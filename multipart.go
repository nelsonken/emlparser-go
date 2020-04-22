// Handle multipart messages.

package emlparser

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"regexp"
)

type Part struct {
	Type    string
	Charset string
	Data    []byte
	Headers map[string][]string
}

// Parse the body of a message, using the given content-type. If the content
// type is multipart, the parts slice will contain an entry for each part
// present; otherwise, it will contain a single entry, with the entire (raw)
// message contents.
func parseBody(ct string, body []byte) (parts []Part, err error) {
	_, ps, err := mime.ParseMediaType(ct)
	if err != nil {
		return
	}

	// if mt != "multipart/alternative" {
	// 	parts = append(parts, Part{ct, body, nil})
	// 	return
	// }

	boundary, ok := ps["boundary"]
	if !ok {
		return nil, errors.New("multipart specified without boundary")
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	p, err := r.NextPart()
	for err == nil {
		data, _ := ioutil.ReadAll(p) // ignore error
		var subparts []Part
		subparts, err = parseBody(p.Header["Content-Type"][0], data)
		//if err == nil then body have sub multipart, and append him
		if err == nil {
			parts = append(parts, subparts...)
		} else {
			contenttype := regexp.MustCompile("(?is)charset=(.*)").FindStringSubmatch(p.Header["Content-Type"][0])
			charset := "UTF-8"
			if len(contenttype) > 1 {
				charset = contenttype[1]
			}
			part := Part{p.Header["Content-Type"][0], charset, data, p.Header}
			parts = append(parts, part)
		}
		p, err = r.NextPart()
	}
	if err == io.EOF {
		err = nil
	}
	return
}
