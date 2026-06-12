package codex

import "github.com/codingagentprotocol/capd/internal/proc"

const macOSAppBundleBin = "/Applications/Codex.app/Contents/Resources/codex"

var resolveBin = proc.ResolveBin

func binPath() string {
	return resolveBin("codex", macOSAppBundleBin)
}
