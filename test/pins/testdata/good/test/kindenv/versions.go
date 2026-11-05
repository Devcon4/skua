package kindenv

const (
	BusyboxImage  = "busybox:1.37@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"
	NodeImage     = "kindest/node:v1.29.14@sha256:8703bd94ee24e51b778d5556ae310c6c0fa67d761fae6379c8e0bb480e6fea29"
	RegistryImage = "localhost:5000/skua@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"
	DexIssuer     = "http://dex.127.0.0.1.nip.io:32000"
	Version       = "v1.0.9"
	IngressDigest = "sha256:594ceea76b01c592858f803f9ff4d2cb40542cae2060410b2c95f75907d659e1"
	Attempts      = 5
)
