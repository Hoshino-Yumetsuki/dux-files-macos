//go:build unix

package wlclipboard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/duxlabs-dev/dux-go/wayland/ext_data_control"
)

// ErrNoData reports an empty selection: no client owns the clipboard or the
// owner advertises no readable mime type.
var ErrNoData = errors.New("no clipboard data")

// ErrTooLarge reports a selection past MaxPasteBytes.
var ErrTooLarge = errors.New("clipboard data too large")

// MaxPasteBytes caps what Paste reads into memory; a source that keeps the
// pipe open past PasteTimeout fails instead of blocking the caller forever.
var (
	MaxPasteBytes int64 = 256 << 20
	PasteTimeout        = 10 * time.Second
)

type Offer struct {
	MimeType string
	Data     []byte
}

// textMimeAliases are offered alongside plain-text content so legacy X11
// clients bridged through XWayland find a target they can convert.
var textMimeAliases = []string{
	"text/plain",
	"text/plain;charset=utf-8",
	"UTF8_STRING",
	"STRING",
	"TEXT",
}

// ExpandOffers turns raw clipboard data into the full offer list to serve,
// adding the standard alias set for text content.
func ExpandOffers(data []byte, mimeType string) []Offer {
	offers := []Offer{{MimeType: mimeType, Data: data}}
	if mimeType != "text/plain" && mimeType != "text/plain;charset=utf-8" {
		return offers
	}
	for _, alias := range textMimeAliases {
		if alias == mimeType {
			continue
		}
		offers = append(offers, Offer{MimeType: alias, Data: data})
	}
	return offers
}

// TextOffers is the offer list for plain text: UTF-8 text/plain plus the
// legacy X11 aliases.
func TextOffers(text string) []Offer {
	return ExpandOffers([]byte(text), "text/plain;charset=utf-8")
}

// Paste reads the selection in the first text type it offers, else an image,
// else whatever comes first.
func Paste() ([]byte, string, error) {
	return PasteSelect(selectPreferredMimeType)
}

// PasteSelect reads the selection in the type choose picks from the offered
// list; returning "" declines the selection and reports ErrNoData.
func PasteSelect(choose func(offered []string) string) ([]byte, string, error) {
	s, err := connectSession()
	if err != nil {
		return nil, "", err
	}
	defer s.Close()

	dataControlMgr, err := s.requireDataControl()
	if err != nil {
		return nil, "", err
	}

	device, err := dataControlMgr.GetDataDevice(s.seat)
	if err != nil {
		return nil, "", fmt.Errorf("get data device: %w", err)
	}
	defer device.Destroy()

	offerMimeTypes := make(map[*ext_data_control.ExtDataControlOfferV1][]string)

	device.SetDataOfferHandler(func(e ext_data_control.ExtDataControlDeviceV1DataOfferEvent) {
		if e.Id == nil {
			return
		}
		offerMimeTypes[e.Id] = nil
		e.Id.SetOfferHandler(func(me ext_data_control.ExtDataControlOfferV1OfferEvent) {
			offerMimeTypes[e.Id] = append(offerMimeTypes[e.Id], me.MimeType)
		})
	})

	var selectionOffer *ext_data_control.ExtDataControlOfferV1
	gotSelection := false

	device.SetSelectionHandler(func(e ext_data_control.ExtDataControlDeviceV1SelectionEvent) {
		selectionOffer = e.Id
		gotSelection = true
	})

	s.display.Roundtrip()
	s.display.Roundtrip()

	if !gotSelection || selectionOffer == nil {
		return nil, "", ErrNoData
	}

	selectedMime := choose(offerMimeTypes[selectionOffer])
	if selectedMime == "" {
		return nil, "", ErrNoData
	}

	r, w, err := os.Pipe()
	if err != nil {
		return nil, "", fmt.Errorf("create pipe: %w", err)
	}
	defer r.Close()

	if err := selectionOffer.Receive(selectedMime, int(w.Fd())); err != nil {
		w.Close()
		return nil, "", fmt.Errorf("receive: %w", err)
	}
	w.Close()

	s.display.Roundtrip()

	_ = r.SetReadDeadline(time.Now().Add(PasteTimeout))
	data, err := io.ReadAll(io.LimitReader(r, MaxPasteBytes+1))
	switch {
	case err != nil:
		return nil, "", fmt.Errorf("read: %w", err)
	case int64(len(data)) > MaxPasteBytes:
		return nil, "", ErrTooLarge
	}

	return data, selectedMime, nil
}

func PasteText() (string, error) {
	data, _, err := Paste()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func selectPreferredMimeType(mimes []string) string {
	preferred := []string{
		"text/plain;charset=utf-8",
		"text/plain",
		"UTF8_STRING",
		"STRING",
		"TEXT",
		"image/png",
		"image/jpeg",
	}

	for _, pref := range preferred {
		for _, mime := range mimes {
			if mime == pref {
				return mime
			}
		}
	}

	if len(mimes) > 0 {
		return mimes[0]
	}
	return ""
}
