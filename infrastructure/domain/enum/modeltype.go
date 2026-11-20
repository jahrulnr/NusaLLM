package enum

// ModelType is the kind of model a gateway's catalog declares. Only text
// models serve a chat turn — see entity.Model.ServesChat.
type ModelType string

const (
	ModelTypeText          ModelType = "text"
	ModelTypeEmbedding     ModelType = "embedding"
	ModelTypeVideo         ModelType = "video"         // text/image/sound to video
	ModelTypeImage         ModelType = "image"         // text/image to image
	ModelTypeSpeech        ModelType = "speech"        // text to speech (TTS)
	ModelTypeTranscription ModelType = "transcription" // speech/audio to text (STT)
	ModelTypeAudio         ModelType = "audio"         // other audio (music, audio understanding)
	ModelTypeRerank        ModelType = "rerank"
	ModelTypeDecision      ModelType = "decision" // answers only from caller-supplied choices
	ModelTypeRealtime      ModelType = "realtime" // live bidirectional session (voice/text), not a request/response turn
)

func (t ModelType) String() string {
	return string(t)
}

func ParseModelType(v string) (ModelType, bool) {
	switch t := ModelType(v); t {
	case ModelTypeText, ModelTypeEmbedding, ModelTypeVideo, ModelTypeImage,
		ModelTypeSpeech, ModelTypeTranscription, ModelTypeAudio,
		ModelTypeRerank, ModelTypeDecision, ModelTypeRealtime:
		return t, true
	}
	return "", false
}
