package main

func mergePatch(original, patch any) any {
	patchMap, patchIsMap := patch.(map[string]any)
	if !patchIsMap {
		return patch
	}
	originalMap, originalIsMap := original.(map[string]any)
	if !originalIsMap {
		originalMap = make(map[string]any)
	}
	result := make(map[string]any)
	for k, v := range originalMap {
		result[k] = v
	}
	for key, patchAt := range patchMap {
		if patchAt == nil {
			delete(result, key)
		} else if originalAt, exists := result[key]; exists {
			result[key] = mergePatch(originalAt, patchAt)
		} else {
			result[key] = patchAt
		}
	}
	return result
}
