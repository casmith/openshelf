package form

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxFormBytes = 1 << 20

// Form wraps submitted values and accumulated validation errors.
type Form struct {
	Values url.Values
	Errors map[string]string
}

func New(v url.Values) *Form {
	if v == nil {
		v = url.Values{}
	}
	return &Form{Values: v, Errors: map[string]string{}}
}

func Parse(w http.ResponseWriter, r *http.Request) (*Form, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return New(r.PostForm), nil
}

func (f *Form) Get(key string) string { return strings.TrimSpace(f.Values.Get(key)) }

func (f *Form) Bool(key string) bool {
	switch f.Get(key) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// Optional returns nil for an empty value.
func (f *Form) Optional(key string) *string {
	if v := f.Get(key); v != "" {
		return &v
	}
	return nil
}

func (f *Form) Add(key, msg string) {
	if _, exists := f.Errors[key]; !exists {
		f.Errors[key] = msg
	}
}

func (f *Form) Error(key string) string { return f.Errors[key] }

func (f *Form) Valid() bool { return len(f.Errors) == 0 }

func (f *Form) Required(keys ...string) {
	for _, k := range keys {
		if f.Get(k) == "" {
			f.Add(k, "This field is required.")
		}
	}
}

func (f *Form) MaxLen(key string, n int) {
	if utf8.RuneCountInString(f.Get(key)) > n {
		f.Add(key, "Must be at most "+strconv.Itoa(n)+" characters.")
	}
}

func (f *Form) MinLen(key string, n int) {
	if v := f.Get(key); v != "" && utf8.RuneCountInString(v) < n {
		f.Add(key, "Must be at least "+strconv.Itoa(n)+" characters.")
	}
}

func (f *Form) OneOf(key string, allowed ...string) {
	v := f.Get(key)
	if v == "" {
		return
	}
	for _, a := range allowed {
		if v == a {
			return
		}
	}
	f.Add(key, "Invalid choice.")
}

// Int parses an optional integer within [min, max].
func (f *Form) Int(key string, min, max int) *int {
	v := f.Get(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		f.Add(key, "Must be a whole number between "+strconv.Itoa(min)+" and "+strconv.Itoa(max)+".")
		return nil
	}
	return &n
}

// Date parses an optional YYYY-MM-DD value.
func (f *Form) Date(key string) *time.Time {
	v := f.Get(key)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, v)
	if err != nil {
		f.Add(key, "Must be a date (YYYY-MM-DD).")
		return nil
	}
	return &t
}
