package iso8583

import (
    "encoding/json"
    "errors"
    "fmt"
    "reflect"
    "strconv"
    "strings"
    "sync"
    "sort"

    iso8583errors "github.com/moov-io/iso8583/errors"
    "github.com/moov-io/iso8583/field"
    "github.com/moov-io/iso8583/utils"
)

var _ json.Marshaler = (*Message)(nil)
var _ json.Unmarshaler = (*Message)(nil)

const (
	mtiIdx    = 0
	bitmapIdx = 1
)

type Message struct {
	spec         *MessageSpec
	cachedBitmap *field.Bitmap

	// stores all fields according to the spec
	fields map[int]field.Field

	// to guard fieldsMap
	mu sync.Mutex

	// tracks which fields were set
	fieldsMap map[int]struct{}
}

func NewMessage(spec *MessageSpec) *Message {
	// Validate the spec
	if err := spec.Validate(); err != nil {
		panic(err) //nolint:forbidigo,nolintlint // as specs moslty static, we panic on spec validation errors
	}

	fields := spec.CreateMessageFields()

	return &Message{
		fields:    fields,
		spec:      spec,
		fieldsMap: map[int]struct{}{},
	}
}

// Deprecated. Use Marshal instead.
func (m *Message) SetData(data interface{}) error {
	return m.Marshal(data)
}

func (m *Message) Bitmap() *field.Bitmap {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.bitmap()
}

// bitmap creates and returns the bitmap field, it's not thread safe
// and should be called from a thread safe function
func (m *Message) bitmap() *field.Bitmap {
	if m.cachedBitmap != nil {
		return m.cachedBitmap
	}

	// We validate the presence and type of the bitmap field in
	// spec.Validate() when we create the message so we can safely assume
	// it exists and is of the correct type
	m.cachedBitmap, _ = m.fields[bitmapIdx].(*field.Bitmap)
	m.cachedBitmap.Reset()

	m.fieldsMap[bitmapIdx] = struct{}{}

	return m.cachedBitmap
}

func (m *Message) MTI(val string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.fieldsMap[mtiIdx] = struct{}{}
	m.fields[mtiIdx].SetBytes([]byte(val))
}

func (m *Message) GetSpec() *MessageSpec {
	return m.spec
}

func (m *Message) Field(id int, val string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if f, ok := m.fields[id]; ok {
		m.fieldsMap[id] = struct{}{}
		return f.SetBytes([]byte(val))
	}
	return fmt.Errorf("failed to set field %d. ID does not exist", id)
}

func (m *Message) BinaryField(id int, val []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if f, ok := m.fields[id]; ok {
		m.fieldsMap[id] = struct{}{}
		return f.SetBytes(val)
	}
	return fmt.Errorf("failed to set binary field %d. ID does not exist", id)
}

func (m *Message) GetMTI() (string, error) {
	// we validate the presence and type of the mti field in
	// spec.Validate() when we create the message so we can safely assume
	// it exists
	return m.fields[mtiIdx].String()
}

func (m *Message) GetString(id int) (string, error) {
	if f, ok := m.fields[id]; ok {
		// m.fieldsMap[id] = struct{}{}
		return f.String()
	}
	return "", fmt.Errorf("failed to get string for field %d. ID does not exist", id)
}

func (m *Message) GetBytes(id int) ([]byte, error) {
	if f, ok := m.fields[id]; ok {
		// m.fieldsMap[id] = struct{}{}
		return f.Bytes()
	}
	return nil, fmt.Errorf("failed to get bytes for field %d. ID does not exist", id)
}

func (m *Message) GetField(id int) field.Field {
	return m.fields[id]
}

// Fields returns the map of the set fields
func (m *Message) GetFields() map[int]field.Field {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.getFields()
}

// getFields returns the map of the set fields. It assumes that the mutex
// is already locked by the caller.
func (m *Message) getFields() map[int]field.Field {
	fields := map[int]field.Field{}
	for i := range m.fieldsMap {
		fields[i] = m.GetField(i)
	}
	return fields
}

// Pack locks the message, packs its fields, and then unlocks it.
// If any errors are encountered during packing, they will be wrapped
// in a *PackError before being returned.
func (m *Message) Pack() ([]byte, error) {
    m.mu.Lock()
    defer m.mu.Unlock()

    // delegate to wrapErrorPack which handles error wrapping
    return m.wrapErrorPack()
}

// wrapErrorPack calls the core packing logic and wraps any errors in a
// *PackError. It assumes that the mutex is already locked by the caller.
func (m *Message) wrapErrorPack() ([]byte, error) {
    // call core packing logic
    data, err := m.pack()
    if err != nil {
        // wrap any error with PackError as per spec
        return nil, &iso8583errors.PackError{Err: err}
    }
    return data, nil
}

// pack contains the core logic for packing the message. This method does not
// handle locking or error wrapping and should typically be used internally
// after ensuring concurrency safety.
func (m *Message) pack() ([]byte, error) {
    // reset bitmap to clear previous state
    bmap := m.bitmap()
    // bitmap() call resets and adds bitmap to fieldsMap
    // Determine packable field IDs
    fieldIDs, err := m.packableFieldIDs()
    if err != nil {
        return nil, fmt.Errorf("failed to determine packable fields: %w", err)
    }

    // set bits in the bitmap for each field except MTI, bitmap itself and presence bits
    for _, id := range fieldIDs {
        // skip MTI and Bitmap indices when setting bits
        if id == mtiIdx || id == bitmapIdx {
            continue
        }
        // skip bitmap presence indicator bits
        if bmap.IsBitmapPresenceBit(id) {
            continue
        }
        bmap.Set(id)
    }

    // buffer to accumulate packed bytes
    var buf []byte

    // pack fields in ascending order
    for _, id := range fieldIDs {
        // handle presence bits: only pack actual data fields and MTI and bitmap
        if id != mtiIdx && id != bitmapIdx && bmap.IsBitmapPresenceBit(id) {
            // skip presence bits beyond the first bitmap
            continue
        }

        // retrieve the field from the spec
        messageField, ok := m.fields[id]
        if !ok || messageField == nil {
            return nil, fmt.Errorf("failed to pack field %d: no specification found", id)
        }

        // pack the field
        packed, perr := messageField.Pack()
        if perr != nil {
            // get description if available for error context
            desc := ""
            if messageField.Spec() != nil {
                desc = messageField.Spec().Description
            }
            if desc == "" {
                // fallback to tag ID string if no description
                desc = strconv.Itoa(id)
            }
            return nil, fmt.Errorf("failed to pack field %d (%s): %w", id, desc, perr)
        }
        buf = append(buf, packed...)
    }

    return buf, nil
}

// Unpack unpacks the message from the given byte slice or returns an error
// which is of type *UnpackError and contains the raw message
func (m *Message) Unpack(src []byte) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    return m.wrapErrorUnpack(src)
}

// wrapErrorUnpack calls the core unpacking logic and wraps any
// errors in a *UnpackError. It assumes that the mutex is already
// locked by the caller.
func (m *Message) wrapErrorUnpack(src []byte) error {
    fieldID, err := m.unpack(src)
    if err != nil {
        // wrap the error in UnpackError with field ID and raw message
        return &iso8583errors.UnpackError{
            Err:        err,
            FieldID:    fieldID,
            RawMessage: src,
        }
    }
    return nil
}

// unpack contains the core logic for unpacking the message. This method does
// not handle locking or error wrapping and should typically be used internally
// after ensuring concurrency safety.
func (m *Message) unpack(src []byte) (string, error) {
    // reset state: clear fieldsMap and bitmap
    m.fieldsMap = map[int]struct{}{}
    bmap := m.bitmap()
    // bitmap() call resets the bitmap
    bmap.Reset()

    // initialize offset
    off := 0

    // 1. Unpack MTI
    if m.fields[mtiIdx] == nil {
        return "0", fmt.Errorf("failed to unpack MTI: no specification found")
    }
    read, err := m.fields[mtiIdx].Unpack(src[off:])
    if err != nil {
        return "0", fmt.Errorf("failed to unpack MTI: %w", err)
    }
    off += read
    m.fieldsMap[mtiIdx] = struct{}{}

    // 2. Unpack Bitmap
    if m.fields[bitmapIdx] == nil {
        return "1", fmt.Errorf("failed to unpack bitmap: no specification found")
    }
    read, err = m.fields[bitmapIdx].Unpack(src[off:])
    if err != nil {
        return "1", fmt.Errorf("failed to unpack bitmap: %w", err)
    }
    off += read
    m.fieldsMap[bitmapIdx] = struct{}{}

    // update cached bitmap pointer after unpacking
    bmap = m.bitmap()

    // 3. Unpack data fields indicated by bitmap
    maxField := bmap.Len()
    for id := 2; id <= maxField; id++ {
        // skip bitmap presence indicator bits
        if bmap.IsBitmapPresenceBit(id) {
            continue
        }
        if !bmap.IsSet(id) {
            continue
        }
        // ensure specification has this field
        messageField, ok := m.fields[id]
        if !ok || messageField == nil {
            return strconv.Itoa(id), fmt.Errorf("failed to unpack field %d: no specification found", id)
        }
        // unpack field
        bytesRead, uerr := messageField.Unpack(src[off:])
        if uerr != nil {
            desc := ""
            if messageField.Spec() != nil {
                desc = messageField.Spec().Description
            }
            if desc == "" {
                desc = strconv.Itoa(id)
            }
            return strconv.Itoa(id), fmt.Errorf("failed to unpack field %d (%s): %w", id, desc, uerr)
        }
        off += bytesRead
        m.fieldsMap[id] = struct{}{}
    }
    return "", nil
}

func (m *Message) MarshalJSON() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// by packing the message we will generate bitmap
	// create HEX representation
	// and validate message against the spec
	if _, err := m.wrapErrorPack(); err != nil {
		return nil, err
	}

	fieldMap := m.getFields()
	strFieldMap := map[string]field.Field{}
	for id, field := range fieldMap {
		strFieldMap[strconv.Itoa(id)] = field
	}

	// get only fields that were set
	bytes, err := json.Marshal(field.OrderedMap(strFieldMap))
	if err != nil {
		return nil, utils.NewSafeError(err, "failed to JSON marshal map to bytes")
	}
	return bytes, nil
}

func (m *Message) UnmarshalJSON(b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var data map[string]json.RawMessage
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}

	for id, rawMsg := range data {
		i, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("failed to unmarshal field %v: could not convert to int", i)
		}

		field, ok := m.fields[i]
		if !ok {
			return fmt.Errorf("failed to unmarshal field %d: no specification found", i)
		}

		if err := json.Unmarshal(rawMsg, field); err != nil {
			return utils.NewSafeErrorf(err, "failed to unmarshal field %v", id)
		}

		m.fieldsMap[i] = struct{}{}
	}

	return nil
}

func (m *Message) packableFieldIDs() ([]int, error) {
    // include bitmap (always) and any field IDs present in fieldsMap
    ids := make([]int, 0, len(m.fieldsMap)+1)
    // ensure bitmap is included
    ids = append(ids, bitmapIdx)
    for id := range m.fieldsMap {
        if id == bitmapIdx {
            continue
        }
        ids = append(ids, id)
    }
    // sort ascending
    sort.Ints(ids)
    return ids, nil
}

// Clone clones the message by creating a new message from the binary
// representation of the original message
func (m *Message) Clone() (*Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	newMessage := NewMessage(m.spec)

	bytes, err := m.wrapErrorPack()
	if err != nil {
		return nil, err
	}

	mti, err := m.GetMTI()
	if err != nil {
		return nil, err
	}

	newMessage.MTI(mti)
	newMessage.Unpack(bytes)

	_, err = newMessage.Pack()
	if err != nil {
		return nil, err
	}

	return newMessage, nil
}

// Marshal populates message fields with v struct field values. It traverses
// through the message fields and calls Unmarshal(...) on them setting the v If
// v is not a struct or not a pointer to struct then it returns error.
func (m *Message) Marshal(v interface{}) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    if v == nil {
        return nil
    }

    rv := reflect.ValueOf(v)
    // dereference pointers and interfaces
    for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
        if rv.IsNil() {
            return fmt.Errorf("data is not a struct")
        }
        rv = rv.Elem()
    }
    if rv.Kind() != reflect.Struct {
        return fmt.Errorf("data is not a struct")
    }
    // traverse struct fields and marshal
    if err := m.marshalStruct(rv); err != nil {
        return err
    }
    return nil
}

// marshalStruct is a helper method that handles the core logic of marshaling a struct.
// It supports anonymous embedded structs by recursively traversing into them when they
// don't have index tags themselves.
func (m *Message) marshalStruct(dataStruct reflect.Value) error {
    // ensure we have a struct
    if dataStruct.Kind() == reflect.Ptr || dataStruct.Kind() == reflect.Interface {
        if dataStruct.IsNil() {
            return nil
        }
        dataStruct = dataStruct.Elem()
    }
    if dataStruct.Kind() != reflect.Struct {
        return nil
    }
    t := dataStruct.Type()
    for i := 0; i < dataStruct.NumField(); i++ {
        sf := t.Field(i)
        fv := dataStruct.Field(i)
        // skip unexported non-anonymous fields
        if sf.PkgPath != "" && !sf.Anonymous {
            continue
        }
        indexTag := field.NewIndexTag(sf)
        if indexTag.ID >= 0 {
            // retrieve message field from spec
            msgField, ok := m.fields[indexTag.ID]
            if !ok || msgField == nil {
                return fmt.Errorf("no message field defined by spec with index: %d", indexTag.ID)
            }
            // skip zero-value unless keepzero tag is set
            if fv.IsZero() && !indexTag.KeepZero {
                continue
            }
            // determine argument for Marshal
            var arg interface{}
            switch fv.Kind() { //nolint:exhaustive
            case reflect.Ptr, reflect.Interface:
                arg = fv.Interface()
            case reflect.Struct:
                if fv.CanAddr() {
                    arg = fv.Addr().Interface()
                } else {
                    arg = fv.Interface()
                }
            default:
                arg = fv.Interface()
            }
            // call message field marshal
            if err := msgField.Marshal(arg); err != nil {
                return fmt.Errorf("failed to set value to field %d: %w", indexTag.ID, err)
            }
            m.fieldsMap[indexTag.ID] = struct{}{}
            continue
        }
        // handle embedded anonymous structs without index tag
        if sf.Anonymous {
            embeddedVal := fv
            // dereference pointers or interfaces
            if embeddedVal.Kind() == reflect.Ptr || embeddedVal.Kind() == reflect.Interface {
                if embeddedVal.IsNil() {
                    continue
                }
                embeddedVal = embeddedVal.Elem()
            }
            if embeddedVal.Kind() == reflect.Struct {
                if err := m.marshalStruct(embeddedVal); err != nil {
                    return err
                }
            }
        }
    }
    return nil
}

// Unmarshal populates v struct fields with message field values. It traverses
// through the message fields and calls Unmarshal(...) on them setting the v If
// v  is nil or not a pointer it returns error.
func (m *Message) Unmarshal(v interface{}) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    if v == nil {
        return fmt.Errorf("data is not a pointer or nil")
    }
    rv := reflect.ValueOf(v)
    if rv.Kind() != reflect.Ptr {
        return fmt.Errorf("data is not a pointer or nil")
    }
    if rv.IsNil() {
        return fmt.Errorf("data is not a pointer or nil")
    }
    // ensure pointer to struct
    elem := rv.Elem()
    if elem.Kind() != reflect.Struct {
        return fmt.Errorf("data is not a struct")
    }
    // unmarshal fields into struct
    if err := m.unmarshalStruct(rv); err != nil {
        return err
    }
    return nil
}

// unmarshalStruct is a helper method that handles the core logic of unmarshaling into a struct.
// It supports anonymous embedded structs by recursively traversing into them when they
// don't have index tags themselves.
func (m *Message) unmarshalStruct(dataStruct reflect.Value) error {
    // dataStruct may be pointer or interface or struct
    if dataStruct.Kind() == reflect.Ptr || dataStruct.Kind() == reflect.Interface {
        if dataStruct.IsNil() {
            return nil
        }
        dataStruct = dataStruct.Elem()
    }
    if dataStruct.Kind() != reflect.Struct {
        return nil
    }
    t := dataStruct.Type()
    for i := 0; i < dataStruct.NumField(); i++ {
        sf := t.Field(i)
        fv := dataStruct.Field(i)
        // skip unexported non-anonymous fields
        if sf.PkgPath != "" && !sf.Anonymous {
            continue
        }
        indexTag := field.NewIndexTag(sf)
        if indexTag.ID >= 0 {
            // get message field
            msgField := m.fields[indexTag.ID]
            if msgField == nil {
                continue
            }
            // skip if field not set in message
            if _, ok := m.fieldsMap[indexTag.ID]; !ok {
                continue
            }
            switch fv.Kind() { //nolint:exhaustive
            case reflect.Ptr:
                if fv.IsNil() {
                    newVal := reflect.New(fv.Type().Elem())
                    if fv.CanSet() {
                        fv.Set(newVal)
                    }
                }
                if err := msgField.Unmarshal(fv.Interface()); err != nil {
                    return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
                }
            case reflect.Interface:
                if fv.IsNil() {
                    continue
                }
                if err := msgField.Unmarshal(fv.Interface()); err != nil {
                    return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
                }
            case reflect.Slice:
                if err := msgField.Unmarshal(fv); err != nil {
                    return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
                }
            default:
                if err := msgField.Unmarshal(fv); err != nil {
                    return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
                }
            }
            continue
        }
        // handle embedded anonymous structs without index tag
        if sf.Anonymous {
            embeddedVal := fv
            if embeddedVal.Kind() == reflect.Ptr {
                if embeddedVal.IsNil() {
                    newVal := reflect.New(embeddedVal.Type().Elem())
                    if embeddedVal.CanSet() {
                        embeddedVal.Set(newVal)
                    }
                }
            }
            if err := m.unmarshalStruct(embeddedVal); err != nil {
                return err
            }
        }
    }
    return nil
}

// UnsetField marks the field with the given ID as not set and replaces it with
// a new zero-valued field. This effectively removes the field's value and excludes
// it from operations like Pack() or Marshal().
func (m *Message) UnsetField(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.fieldsMap[id]; ok {
		delete(m.fieldsMap, id)
		// re-create the field to reset its value (and subfields if it's a composite field)
		if fieldSpec, ok := m.GetSpec().Fields[id]; ok {
			m.fields[id] = createMessageField(fieldSpec)
		}
	}
}

// UnsetFields marks multiple fields identified by their paths as not set and
// replaces them with new zero-valued fields. Each path should be in the format
// "a.b.c". This effectively removes the fields' values and excludes them from
// operations like Pack() or Marshal().
func (m *Message) UnsetFields(idPaths ...string) error {
	for _, idPath := range idPaths {
		if idPath == "" {
			continue
		}

		id, path, _ := strings.Cut(idPath, ".")
		idx, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("conversion of %s to int failed: %w", id, err)
		}

		if _, ok := m.fieldsMap[idx]; ok {
			if len(path) == 0 {
				m.UnsetField(idx)
				continue
			}

			f := m.fields[idx]
			if f == nil {
				return fmt.Errorf("field %d does not exist", idx)
			}

			composite, ok := f.(*field.Composite)
			if !ok {
				return fmt.Errorf("field %d is not a composite field and its subfields %s cannot be unset", idx, path)
			}

			if err := composite.UnsetSubfields(path); err != nil {
				return fmt.Errorf("failed to unset %s in composite field %d: %w", path, idx, err)
			}
		}
	}

	return nil
}
