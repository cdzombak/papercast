// Package tts synthesizes speech via Google Cloud Text-to-Speech, with
// retry and cache-key helpers.
package tts

import (
	"context"
	"fmt"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

// Request describes a single synthesis request.
type Request struct {
	Payload      string // plain text or a complete <speak> SSML document
	SSML         bool
	Voice        string // e.g. "en-US-Chirp3-HD-Aoede"
	LanguageCode string // e.g. "en-US"
}

// Synthesizer converts a Request into MP3 audio.
type Synthesizer interface {
	Synthesize(ctx context.Context, req Request) ([]byte, error) // MP3 bytes
	Close() error
}

type googleSynthesizer struct {
	client *texttospeech.Client
}

// NewGoogleSynthesizer creates a Synthesizer backed by Google Cloud TTS.
// Credentials come from Application Default Credentials (the caller sets
// GOOGLE_APPLICATION_CREDENTIALS beforehand).
func NewGoogleSynthesizer(ctx context.Context) (Synthesizer, error) {
	client, err := texttospeech.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Google TTS client: %w", err)
	}
	return &googleSynthesizer{client: client}, nil
}

func (g *googleSynthesizer) Synthesize(ctx context.Context, req Request) ([]byte, error) {
	input := &texttospeechpb.SynthesisInput{}
	if req.SSML {
		input.InputSource = &texttospeechpb.SynthesisInput_Ssml{Ssml: req.Payload}
	} else {
		input.InputSource = &texttospeechpb.SynthesisInput_Text{Text: req.Payload}
	}
	resp, err := g.client.SynthesizeSpeech(ctx, &texttospeechpb.SynthesizeSpeechRequest{
		Input: input,
		Voice: &texttospeechpb.VoiceSelectionParams{
			LanguageCode: req.LanguageCode,
			Name:         req.Voice,
		},
		AudioConfig: &texttospeechpb.AudioConfig{
			AudioEncoding: texttospeechpb.AudioEncoding_MP3,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("synthesize speech (voice %s): %w", req.Voice, err)
	}
	return resp.GetAudioContent(), nil
}

func (g *googleSynthesizer) Close() error {
	return g.client.Close()
}
