package hyperv

import "encoding/json"

func mergeConfig(raw json.RawMessage, set map[string]any) (json.RawMessage, error) {
	m := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
	}
	for k, v := range set {
		m[k] = v
	}
	return json.Marshal(m)
}
