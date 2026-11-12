// Package serializer 提供内置序列化器，也可自行实现 cachehub.Serializer。
package serializer

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type JSON struct{}

func (JSON) Encode(value any) ([]byte, error) { return json.Marshal(value) }
func (JSON) Decode(data []byte, target any) error {
	if !json.Valid(data) {
		return fmt.Errorf("无效的 JSON 缓存数据")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}
