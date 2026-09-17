package english

import "regexp"

var (
	reAllowInsecure    = regexp.MustCompile(`AllowInsecure\s*=\s*true`)
	reInsecureBind     = regexp.MustCompile(`0\.0\.0\.0(?::\d+)?`)
	reAuthorizationLog = regexp.MustCompile(`Authorization`) // used in context of logging
	reBearerToken      = regexp.MustCompile(`Authorization:\s*Bearer\s*%s`)
	rePrivateKey       = regexp.MustCompile(`(?i)BEGIN PRIVATE KEY`)
	reMnemonic         = regexp.MustCompile(`(?i)(mnemonic|seed|passphrase)`)
	reJWTDisable       = regexp.MustCompile(`(?i)jwt.*(disable|skipverify)`)
	reFileServer       = regexp.MustCompile(`http\.FileServer|http\.ServeFile`)
	rePathJoinUser     = regexp.MustCompile(`filepath\.Join\([^\)]*user`)
	reNonce            = regexp.MustCompile(`(?i)(nonce|ttl|expiry|antireplay)`)
	reBigIntSub        = regexp.MustCompile(`big\.NewInt\(0\)\.Sub`)
	reFeeBranch        = regexp.MustCompile(`(?i)(fee|zn?hb)`)
	rePause            = regexp.MustCompile(`(?i)pause`)
	reRateLimit        = regexp.MustCompile(`(?i)(rate.?limit|limiter)`)
	reGovulnFinding    = regexp.MustCompile(`(?m)^\s*(?P<path>[^:]+):(\d+):\d+:\s*(?P<msg>.+)$`)
)
