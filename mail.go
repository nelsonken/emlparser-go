// Package mail implements a parser for electronic mail messages as specified
// in RFC5322.
//
// We allow both CRLF and LF to be used in the input, possibly mixed.
package emlparser

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"mime/quotedprintable"
	"regexp"
	"strings"
	"time"

	"github.com/nelsonken/emlparser/decoder"
)

var benc = base64.URLEncoding

func mkId(s []byte) string {
	h := sha1.New()
	h.Write(s)
	hash := h.Sum(nil)
	ed := benc.EncodeToString(hash)
	return ed[0:20]
}

type HeaderInfo struct {
	FullHeaders []Header // all headers
	OptHeaders  []Header // unprocessed headers

	MessageId   string
	Id          string
	Date        time.Time
	From        []Address
	Sender      Address
	ReplyTo     []Address
	To          []Address
	Cc          []Address
	Bcc         []Address
	Subject     string
	Comments    []string
	Keywords    []string
	ContentType string

	InReply    []string
	References []string
}

type Message struct {
	HeaderInfo
	Body        []byte
	Text        string
	Html        string
	Attachments []Attachment
	Parts       []Part
}

type Attachment struct {
	Filename string
	Data     []byte
}

type Header struct {
	Key, Value string
}

func Parse(s []byte) (m Message, e error) {
	r, e := ParseRaw(s)
	if e != nil {
		return
	}
	return Process(r)
}

func Process(r RawMessage) (m Message, e error) {
	m.FullHeaders = []Header{}
	m.OptHeaders = []Header{}
	for _, rh := range r.RawHeaders {
		h := Header{string(rh.Key), string(rh.Value)}
		m.FullHeaders = append(m.FullHeaders, h)
		switch string(rh.Key) {
		case `Content-Type`:
			m.ContentType = string(rh.Value)
		case `Message-ID`:
			v := bytes.Trim(rh.Value, `<>`)
			m.MessageId = string(v)
			m.Id = mkId(v)
		case `In-Reply-To`:
			ids := strings.Fields(string(rh.Value))
			for _, id := range ids {
				m.InReply = append(m.InReply, strings.Trim(id, `<> `))
			}
		case `References`:
			ids := strings.Fields(string(rh.Value))
			for _, id := range ids {
				m.References = append(m.References, strings.Trim(id, `<> `))
			}
		case `Date`:
			m.Date = ParseDate(string(rh.Value))
		case `From`:
			m.From, e = parseAddressList(rh.Value)
		case `Sender`:
			m.Sender, e = ParseAddress(rh.Value)
		case `Reply-To`:
			m.ReplyTo, e = parseAddressList(rh.Value)
		case `To`:
			m.To, e = parseAddressList(rh.Value)
		case `Cc`:
			m.Cc, e = parseAddressList(rh.Value)
		case `Bcc`:
			m.Bcc, e = parseAddressList(rh.Value)
		case `Subject`:
			subject, err := decoder.Parse(rh.Value)
			if err != nil {
				fmt.Println("Failed decode subject", err)
			}
			m.Subject = string(subject)
		case `Comments`:
			m.Comments = append(m.Comments, string(rh.Value))
		case `Keywords`:
			ks := strings.Split(string(rh.Value), ",")
			for _, k := range ks {
				m.Keywords = append(m.Keywords, strings.TrimSpace(k))
			}
		default:
			m.OptHeaders = append(m.OptHeaders, h)
		}
		if e != nil {
			return
		}
	}
	if m.Sender == nil && len(m.From) > 0 {
		m.Sender = m.From[0]
	}

	if m.ContentType != `` {
		parts, er := parseBody(m.ContentType, r.Body)
		if er != nil {
			e = er
			return
		}

		for _, part := range parts {
			switch {
			case strings.Contains(part.Type, "text/plain"):

				data, err := decoder.UTF8(part.Charset, part.Data)
				if err != nil {
					m.Text = string(part.Data)
				} else {
					m.Text = string(data)
				}
			case strings.Contains(part.Type, "text/html"):

				data, err := decoder.UTF8(part.Charset, part.Data)
				if err != nil {
					m.Html = string(part.Data)
				} else {
					m.Html = string(data)
				}

			default:
				if cd, ok := part.Headers["Content-Disposition"]; ok {
					if strings.Contains(cd[0], "attachment") {
						filename := regexp.MustCompile("(?msi)name=\"(.*?)\"").FindStringSubmatch(cd[0]) //.FindString(cd[0])
						if len(filename) < 2 {
							fmt.Println("failed get filename from header content-disposition")
							break
						}

						dfilename, err := decoder.Parse([]byte(filename[1]))
						if err != nil {
							fmt.Println("Failed decode filename of attachment", err)
						} else {
							filename[1] = string(dfilename)
						}

						if encoding, ok := part.Headers["Content-Transfer-Encoding"]; ok {
							switch strings.ToLower(encoding[0]) {
							case "base64":
								part.Data, er = base64.StdEncoding.DecodeString(string(part.Data))
								if er != nil {
									fmt.Println(er, "failed decode base64")
								}
							case "quoted-printable":
								part.Data, _ = ioutil.ReadAll(quotedprintable.NewReader(bytes.NewReader(part.Data)))
							}
						}
						m.Attachments = append(m.Attachments, Attachment{filename[1], part.Data})

					}
				}
			}
		}

		m.Parts = parts
		m.ContentType = parts[0].Type
		m.Text = string(parts[0].Data)
	} else {
		m.Text = string(r.Body)
	}
	return
}

type RawHeader struct {
	Key, Value []byte
}

type RawMessage struct {
	RawHeaders []RawHeader
	Body       []byte
}

func isWSP(b byte) bool {
	return b == ' ' || b == '\t'
}

func ParseRaw(s []byte) (m RawMessage, e error) {
	// parser states
	const (
		READY = iota
		HKEY
		HVWS
		HVAL
	)

	const (
		CR = '\r'
		LF = '\n'
	)
	CRLF := []byte{CR, LF}

	state := READY
	kstart, kend, vstart := 0, 0, 0
	done := false

	m.RawHeaders = []RawHeader{}

	for i := 0; i < len(s); i++ {
		b := s[i]
		switch state {
		case READY:
			if b == CR && i < len(s)-1 && s[i+1] == LF {
				// we are at the beginning of an empty header
				m.Body = s[i+2:]
				done = true
				goto Done
			}
			if b == LF {
				m.Body = s[i+1:]
				done = true
				goto Done
			}
			// otherwise this character is the first in a header
			// key
			kstart = i
			state = HKEY
		case HKEY:
			if b == ':' {
				kend = i
				state = HVWS
			}
		case HVWS:
			if !isWSP(b) {
				vstart = i
				state = HVAL
			}
		case HVAL:
			if b == CR && i < len(s)-2 && s[i+1] == LF && !isWSP(s[i+2]) {
				v := bytes.Replace(s[vstart:i], CRLF, nil, -1)
				hdr := RawHeader{s[kstart:kend], v}
				m.RawHeaders = append(m.RawHeaders, hdr)
				state = READY
				i++
			} else if b == LF && i < len(s)-1 && !isWSP(s[i+1]) {
				v := bytes.Replace(s[vstart:i], CRLF, nil, -1)
				hdr := RawHeader{s[kstart:kend], v}
				m.RawHeaders = append(m.RawHeaders, hdr)
				state = READY
			}
		}
	}
Done:
	if !done {
		e = errors.New("unexpected EOF")
	}
	return
}
