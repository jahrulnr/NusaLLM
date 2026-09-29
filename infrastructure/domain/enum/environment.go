package enum

// Environment identifies where a conversation's workspace and commands run.
// It is separate from the provider's API style and application-data storage.
type Environment string

const (
	EnvironmentLocal  Environment = "local"
	EnvironmentSSH    Environment = "ssh"
	EnvironmentDocker Environment = "docker"
)

func (e Environment) String() string { return string(e) }

func ParseEnvironment(v string) (Environment, bool) {
	switch e := Environment(v); e {
	case EnvironmentLocal, EnvironmentSSH, EnvironmentDocker:
		return e, true
	}
	return "", false
}
