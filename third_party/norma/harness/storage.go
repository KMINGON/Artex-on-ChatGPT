package harness

// StorageError stops a run without replaying potentially completed tool effects.
type StorageError struct{ Err error }

func (e *StorageError) Error() string {
	return "대화 기록을 읽거나 저장하지 못해 실행을 중지했습니다: " + e.Err.Error()
}
func (e *StorageError) Unwrap() error    { return e.Err }
func (e *StorageError) NoReplay() bool   { return true }
func (e *StorageError) NoFallback() bool { return true }

func (l *loop) storageError() error {
	if r, ok := l.in.Recorder.(interface{ Err() error }); ok {
		if err := r.Err(); err != nil {
			return &StorageError{Err: err}
		}
	}
	return nil
}
