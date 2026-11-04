package lab

import (
	"strings"
	"testing"
)

func TestParseSums(t *testing.T) {
	sums := `1207e6e3efaff8ee77ab7a32c156a3eea6a9e53f94cfc48efbd1f49ba554ca36ac62ab27395aa813a1775a8c0a393b1a1ce5377d9e5670f6113e3110c57fee4b  debian-13-generic-amd64.qcow2
04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94 *noble-server-cloudimg-amd64.img
# Rocky-9-GenericCloud-Base.latest.x86_64.qcow2: 645988352 bytes
SHA256 (Rocky-9-GenericCloud-Base.latest.x86_64.qcow2) = 92C206CC6F790C61583247EEFE87890F8828420662C17CACF247CEC78AB4EEC8
`
	for file, prefix := range map[string]string{
		"debian-13-generic-amd64.qcow2":                 "1207e6e3",
		"noble-server-cloudimg-amd64.img":               "04e8f246",
		"Rocky-9-GenericCloud-Base.latest.x86_64.qcow2": "92c206cc",
	} {
		got, err := ParseSums(strings.NewReader(sums), file)
		if err != nil || !strings.HasPrefix(got, prefix) {
			t.Errorf("%s : %q, %v", file, got, err)
		}
	}
	if _, err := ParseSums(strings.NewReader(sums), "absent.qcow2"); err == nil {
		t.Error("fichier absent : erreur attendue")
	}
}
