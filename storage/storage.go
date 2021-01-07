package storage

import (
	"errors"
	"reflect"
)

type Index struct {
	Id string `json:"_id"`
}

type Value struct {
	*Index
	Data interface{}
}

func (v *Value) ToJson() map[string]interface{} {
	result := map[string]interface{}{}
	t := reflect.TypeOf(v.Data)
	val := reflect.ValueOf(v.Data)
	for i := 0; i < t.Elem().NumField(); i++ {
		key := t.Elem().Field(i).Tag.Get("json")
		value := val.Elem().Field(i).Interface()
		if key != "" {
			result[key] = value
		}
	}

	if id, ok := reflect.TypeOf(v.Index).Elem().FieldByName("Id"); ok {
		result[id.Tag.Get("json")] = v.Id
	}

	return result
}

type Values []*Value

func (v Values) ToJson() []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(v))
	for _, val := range v {
		result = append(result, val.ToJson())
	}
	return result
}

type Storage struct {
	Keys   map[string]struct{}
	Values Values
}

func NewStorage(data ...*Value) *Storage {
	keys := map[string]struct{}{}
	values := make([]*Value, 0, len(data))
	for _, item := range data {
		if _, ok := keys[item.Id]; !ok {
			keys[item.Id] = struct{}{}
			values = append(values, item)
		}
	}
	return &Storage{
		Keys:   keys,
		Values: values,
	}
}

func (s *Storage) Add(val interface{}) error {
	index := reflect.ValueOf(val).Elem().FieldByName("Index")
	if index.IsNil() {
		return errors.New("Index can not be nil.")
	}
	id := index.Elem().FieldByName("Id").Interface().(string)
	if _, ok := s.Keys[id]; !ok {
		s.Keys[id] = struct{}{}
		s.Values = append(s.Values, &Value{
			Index: &Index{
				Id: id,
			},
			Data: val,
		})
	} else {
		for i := range s.Values {
			if s.Values[i].Id == id {
				s.Values[i] = &Value{
					Index: &Index{
						Id: id,
					},
					Data: val,
				}
			}
			break
		}
	}
	return nil
}

func (s *Storage) Delete(val *Value) {
	// todo
}
