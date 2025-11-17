package iso8583

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

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

	return m.wrapErrorPack()
}

// wrapErrorPack calls the core packing logic and wraps any errors in a
// *PackError. It assumes that the mutex is already locked by the caller.
func (m *Message) wrapErrorPack() ([]byte, error) {
	packed, err := m.pack()
	if err != nil {
		return nil, &iso8583errors.PackError{
			Err: fmt.Errorf("failed to pack message: %w", err),
		}
	}
	return packed, nil
}

// pack contains the core logic for packing the message. This method does not
// handle locking or error wrapping and should typically be used internally
// after ensuring concurrency safety.
func (m *Message) pack() ([]byte, error) {
	// Reset the bitmap
	m.bitmap().Reset()

	// Get sorted list of field IDs to pack
	ids, err := m.packableFieldIDs()
	if err != nil {
		return nil, err
	}

	// Set bitmap bits for all fields except MTI and bitmap itself
	for _, id := range ids {
		// Skip MTI (0) and bitmap (1)
		if id == mtiIdx || id == bitmapIdx {
			continue
		}

		// Skip bitmap presence indicator bits (1, 65, 129, 193, etc.)
		// These are managed by the bitmap itself
		if m.bitmap().IsBitmapPresenceBit(id) {
			continue
		}

		// Set the bit in the bitmap
		m.bitmap().Set(id)
	}

	// Pack all fields in order
	var packed []byte
	for _, id := range ids {
		// Skip bitmap presence indicator bits (except the first bitmap at index 1)
		// These bits indicate the presence of additional bitmaps, not data fields
		if id != bitmapIdx && m.bitmap().IsBitmapPresenceBit(id) {
			continue
		}

		f, ok := m.fields[id]
		if !ok {
			return nil, fmt.Errorf("failed to pack field %d: no specification found", id)
		}

		fieldData, err := f.Pack()
		if err != nil {
			desc := ""
			if spec := f.Spec(); spec != nil {
				desc = spec.Description
			}
			return nil, fmt.Errorf("failed to pack field %d (%s): %w", id, desc, err)
		}

		packed = append(packed, fieldData...)
	}

	return packed, nil
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
	// Reset state
	m.fieldsMap = map[int]struct{}{}
	m.bitmap().Reset()

	var off int

	// Unpack MTI (index 0)
	read, err := m.fields[mtiIdx].Unpack(src)
	if err != nil {
		return "0", fmt.Errorf("failed to unpack MTI: %w", err)
	}
	m.fieldsMap[mtiIdx] = struct{}{}
	off += read

	// Unpack Bitmap (index 1)
	read, err = m.fields[bitmapIdx].Unpack(src[off:])
	if err != nil {
		return "1", fmt.Errorf("failed to unpack bitmap: %w", err)
	}
	// Bitmap automatically sets itself in fieldsMap
	off += read

	// Unpack data fields
	for i := 2; i <= m.bitmap().Len(); i++ {
		// Skip bitmap presence indicator bits (65, 129, 193, etc.)
		if m.bitmap().IsBitmapPresenceBit(i) {
			continue
		}

		// Check if field is set in the bitmap
		if !m.bitmap().IsSet(i) {
			continue
		}

		// Get the field from the spec
		f, ok := m.fields[i]
		if !ok {
			return strconv.Itoa(i), fmt.Errorf("failed to unpack field %d: no specification found", i)
		}

		// Unpack the field
		read, err = f.Unpack(src[off:])
		if err != nil {
			desc := ""
			if spec := f.Spec(); spec != nil {
				desc = spec.Description
			}
			return strconv.Itoa(i), fmt.Errorf("failed to unpack field %d (%s): %w", i, desc, err)
		}

		// Mark field as set
		m.fieldsMap[i] = struct{}{}
		off += read
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
	// Collect all field IDs from fieldsMap (excluding bitmap)
	var ids []int
	for id := range m.fieldsMap {
		if id != bitmapIdx {
			ids = append(ids, id)
		}
	}

	// Always include bitmap
	ids = append(ids, bitmapIdx)

	// Sort in ascending order
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

	// Return nil if v is nil
	if v == nil {
		return nil
	}

	// Get the reflect.Value of v
	rv := reflect.ValueOf(v)

	// Dereference pointers and interfaces
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}

	// Verify it's a struct
	if rv.Kind() != reflect.Struct {
		return errors.New("data is not a struct")
	}

	return m.marshalStruct(rv)
}

// marshalStruct is a helper method that handles the core logic of marshaling a struct.
// It supports anonymous embedded structs by recursively traversing into them when they
// don't have index tags themselves.
func (m *Message) marshalStruct(dataStruct reflect.Value) error {
	for i := 0; i < dataStruct.NumField(); i++ {
		structField := dataStruct.Type().Field(i)
		dataField := dataStruct.Field(i)

		// Parse the index tag
		indexTag := field.NewIndexTag(structField)

		if indexTag.ID >= 0 {
			// Field has a valid index tag
			messageField, ok := m.fields[indexTag.ID]
			if !ok {
				return fmt.Errorf("no message field defined by spec with index: %d", indexTag.ID)
			}

			// Check if the field is zero value and keepzero is not set
			if !indexTag.KeepZero && dataField.IsZero() {
				continue
			}

			// Marshal the field
			if err := messageField.Marshal(dataField.Interface()); err != nil {
				return fmt.Errorf("failed to set value to field %d: %w", indexTag.ID, err)
			}

			// Mark field as set
			m.fieldsMap[indexTag.ID] = struct{}{}
		} else if structField.Anonymous {
			// Anonymous embedded struct without index tag - recursively marshal it
			// Dereference pointers and interfaces
			fieldValue := dataField
			for fieldValue.Kind() == reflect.Ptr || fieldValue.Kind() == reflect.Interface {
				if fieldValue.IsNil() {
					break
				}
				fieldValue = fieldValue.Elem()
			}

			// If it's a struct, recursively marshal it
			if fieldValue.Kind() == reflect.Struct {
				if err := m.marshalStruct(fieldValue); err != nil {
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

	// Verify v is a pointer and not nil
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return errors.New("data is not a pointer or nil")
	}

	// Dereference to get the struct
	rv = rv.Elem()

	// Verify it's a struct
	if rv.Kind() != reflect.Struct {
		return errors.New("data is not a struct")
	}

	return m.unmarshalStruct(rv)
}

// unmarshalStruct is a helper method that handles the core logic of unmarshaling into a struct.
// It supports anonymous embedded structs by recursively traversing into them when they
// don't have index tags themselves.
func (m *Message) unmarshalStruct(dataStruct reflect.Value) error {
	for i := 0; i < dataStruct.NumField(); i++ {
		structField := dataStruct.Type().Field(i)
		dataField := dataStruct.Field(i)

		// Parse the index tag
		indexTag := field.NewIndexTag(structField)

		if indexTag.ID >= 0 {
			// Field has a valid index tag
			messageField := m.fields[indexTag.ID]
			if messageField == nil {
				// Field not in spec, skip
				continue
			}

			// Check if field is set in the message
			if _, ok := m.fieldsMap[indexTag.ID]; !ok {
				// Field not present in message, skip
				continue
			}

			// Handle different field kinds
			switch dataField.Kind() {
			case reflect.Ptr, reflect.Interface:
				// Initialize nil pointers
				if dataField.IsNil() && dataField.CanSet() {
					dataField.Set(reflect.New(dataField.Type().Elem()))
				}
				if err := messageField.Unmarshal(dataField.Interface()); err != nil {
					return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
				}
			case reflect.Slice:
				// Pass reflect.Value so slice can be modified
				if err := messageField.Unmarshal(dataField); err != nil {
					return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
				}
			default:
				// Native types
				if err := messageField.Unmarshal(dataField); err != nil {
					return fmt.Errorf("failed to get value from field %d: %w", indexTag.ID, err)
				}
			}
		} else if structField.Anonymous {
			// Anonymous embedded struct without index tag - recursively unmarshal it
			fieldValue := dataField

			// Initialize nil pointers if possible
			if fieldValue.Kind() == reflect.Ptr {
				if fieldValue.IsNil() {
					if fieldValue.CanSet() {
						fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
					} else {
						// Can't initialize, skip
						continue
					}
				}
				fieldValue = fieldValue.Elem()
			}

			// Dereference interfaces
			for fieldValue.Kind() == reflect.Interface {
				if fieldValue.IsNil() {
					break
				}
				fieldValue = fieldValue.Elem()
			}

			// If it's a struct, recursively unmarshal it
			if fieldValue.Kind() == reflect.Struct {
				if err := m.unmarshalStruct(fieldValue); err != nil {
					return err
				}
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
