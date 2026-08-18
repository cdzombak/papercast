package tts

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// CacheKey returns a deterministic identifier (64-char hex) for the audio a
// request produces, derived from the full synthesis request: payload, SSML
// flag, voice, language, and the fixed audio encoding parameters.
func CacheKey(req Request) string {
	h := sha256.New()
	writeField := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	writeField("v1")
	writeField("MP3")
	writeField(req.Voice)
	writeField(req.LanguageCode)
	if req.SSML {
		writeField("ssml")
	} else {
		writeField("text")
	}
	writeField(req.Payload)
	return hex.EncodeToString(h.Sum(nil))
}
