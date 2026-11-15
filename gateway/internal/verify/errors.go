package verify

import (
	"encoding/xml"
	"net/http"
)

type Error struct {
	Status  int    `xml:"-"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (e *Error) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(e.Status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: e.Code, Message: e.Message})
}
func signatureError() *Error {
	return &Error{403, "SignatureDoesNotMatch", "The request signature does not match."}
}
func denied(message string) *Error { return &Error{403, "AccessDenied", message} }
func unsupported() *Error          { return &Error{501, "NotImplemented", "This operation is not implemented."} }
func badKey() *Error               { return &Error{400, "InvalidArgument", "Invalid object key."} }
