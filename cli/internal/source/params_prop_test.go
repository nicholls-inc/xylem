package source

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

func taskParamsGen() *rapid.Generator[map[string]any] {
	return rapid.Custom(func(t *rapid.T) map[string]any {
		count := rapid.IntRange(1, 5).Draw(t, "count")
		params := make(map[string]any, count)
		for i := 0; i < count; i++ {
			key := fmt.Sprintf("key_%d", i)
			switch rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("kind_%d", i)) {
			case 0:
				params[key] = rapid.StringMatching(`[a-z_]{1,12}`).Draw(t, fmt.Sprintf("string_%d", i))
			case 1:
				params[key] = rapid.IntRange(0, 2000).Draw(t, fmt.Sprintf("int_%d", i))
			default:
				size := rapid.IntRange(0, 4).Draw(t, fmt.Sprintf("slice_size_%d", i))
				values := make([]any, 0, size)
				for j := 0; j < size; j++ {
					values = append(values, rapid.StringMatching(`[a-z_]{1,12}`).Draw(t, fmt.Sprintf("slice_%d_%d", i, j)))
				}
				params[key] = values
			}
		}
		return params
	})
}

func TestPropTaskParamsEncodeProducesJSONShape(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		params := taskParamsGen().Draw(t, "params")
		encoded, err := EncodeTaskParams(params)
		if err != nil {
			t.Fatalf("EncodeTaskParams() error = %v", err)
		}

		var decoded map[string]any
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}

		for key, want := range params {
			got, ok := decoded[key]
			if !ok {
				t.Fatalf("decoded params missing key %q in %#v", key, decoded)
			}
			switch typed := want.(type) {
			case int:
				if got != float64(typed) {
					t.Fatalf("decoded[%q] = %#v, want %v", key, got, float64(typed))
				}
			default:
				if !reflect.DeepEqual(got, typed) {
					t.Fatalf("decoded[%q] = %#v, want %#v", key, got, typed)
				}
			}
		}
	})
}
