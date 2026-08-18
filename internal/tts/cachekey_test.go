package tts

import (
	"regexp"
	"testing"
)

func TestCacheKey_DeterministicAndHex(t *testing.T) {
	req := Request{
		Payload:      "Hello, world.",
		SSML:         false,
		Voice:        "en-US-Chirp3-HD-Aoede",
		LanguageCode: "en-US",
	}
	k1 := CacheKey(req)
	k2 := CacheKey(req)
	if k1 != k2 {
		t.Errorf("identical requests gave different keys: %s vs %s", k1, k2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(k1) {
		t.Errorf("key %q is not 64 hex chars", k1)
	}
}

func TestCacheKey_FieldChangesChangeKey(t *testing.T) {
	base := Request{
		Payload:      "Hello, world.",
		SSML:         false,
		Voice:        "en-US-Chirp3-HD-Aoede",
		LanguageCode: "en-US",
	}
	baseKey := CacheKey(base)

	variants := map[string]Request{
		"payload":  {Payload: "Goodbye, world.", SSML: base.SSML, Voice: base.Voice, LanguageCode: base.LanguageCode},
		"ssml":     {Payload: base.Payload, SSML: true, Voice: base.Voice, LanguageCode: base.LanguageCode},
		"voice":    {Payload: base.Payload, SSML: base.SSML, Voice: "en-US-Chirp3-HD-Puck", LanguageCode: base.LanguageCode},
		"language": {Payload: base.Payload, SSML: base.SSML, Voice: base.Voice, LanguageCode: "en-GB"},
	}
	for name, req := range variants {
		if CacheKey(req) == baseKey {
			t.Errorf("changing %s did not change the key", name)
		}
	}
}
