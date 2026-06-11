package codex

import "github.com/codingagentprotocol/capd/internal/proc"

const macOSAppBundleBin = "/Applications/Codex.app/Contents/Resources/codex"

func binPath() string {
	return proc.ResolveBin("codex", macOSAppBundleBin)
}
