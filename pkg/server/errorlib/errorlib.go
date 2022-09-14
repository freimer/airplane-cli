package errorlib

type HttpError struct {
	Code int
	Msg  string
}

func (e HttpError) Error() string {
	return e.Msg
}
