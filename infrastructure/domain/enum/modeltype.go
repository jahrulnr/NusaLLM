package enum

// ModelType categorizes a model's modality per the gateway's catalog —
// matching OpenRouter's model categories.
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
	ModelTypeDecision      ModelType = "decision"
)

func (t ModelType) String() string {
	return string(t)
}

func ParseModelType(v string) (ModelType, bool) {
	switch t := ModelType(v); t {
	case ModelTypeText, ModelTypeEmbedding, ModelTypeVideo, ModelTypeImage,
		ModelTypeSpeech, ModelTypeTranscription, ModelTypeAudio,
		ModelTypeRerank, ModelTypeDecision:
		return t, true
	}
	return "", false
}

// ChatCapable reports whether a model of this type answers a chat turn —
// text, image, and audio-understanding models can; the single-purpose
// surfaces (embedding, rerank, speech, transcription, video, decision)
// cannot. The unset type is not "capable": callers decide fail-open for
// catalogs that omit the declaration.
func (t ModelType) ChatCapable() bool {
	switch t {
	case ModelTypeText, ModelTypeImage, ModelTypeAudio:
		return true
	}
	return false
}
