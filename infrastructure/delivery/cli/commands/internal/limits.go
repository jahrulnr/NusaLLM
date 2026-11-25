package commandinternal

// MaxRows caps palette completion materialization.
const MaxRows = 50

// DefaultHistoryLimit is the default /history and /resume transcript window.
const DefaultHistoryLimit = 20

// TitleHistoryEnd limits history loaded when inferring unset list titles from
// the first editable message — avoids loading full transcripts per row.
const TitleHistoryEnd = 64
