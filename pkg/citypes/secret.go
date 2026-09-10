package citypes

import "os"

const Redacted = "[redacted]"

type Secret string

func (Secret) String() string { return Redacted }

func (Secret) GoString() string { return Redacted }

func SecretFromEnv(variable string) Secret { return Secret(os.Getenv(variable)) }
