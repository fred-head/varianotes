package encryption

import (
	"os"
	"reflect"
)

// hasSingleLink reports false when the platform cannot expose a link count.
// Conversion transactions fail closed when they cannot prove that deleting a
// plaintext path will not leave a hard-linked plaintext alias behind.
func hasSingleLink(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	links := value.FieldByName("Nlink")
	if !links.IsValid() {
		return false
	}
	switch links.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return links.Uint() == 1
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return links.Int() == 1
	default:
		return false
	}
}
