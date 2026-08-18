package audio

import (
	"fmt"

	id3v2 "github.com/bogem/id3v2/v2"
)

// WriteID3Tags sets the ID3v2 title and artist tags on the MP3 at path.
func WriteID3Tags(path, title, artist string) error {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("open %s for ID3 tagging: %w", path, err)
	}
	defer func() { _ = tag.Close() }()
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)
	tag.SetTitle(title)
	tag.SetArtist(artist)
	if err := tag.Save(); err != nil {
		return fmt.Errorf("save ID3 tags to %s: %w", path, err)
	}
	return nil
}
