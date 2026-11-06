package lab

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestUnattend(t *testing.T) {
	d := unattendData{Edition: "Windows Server 2022 SERVERSTANDARD", ComputerName: "WIN22", Password: `P@ss<&>"w0rd`,
		TimeZone: "Romance Standard Time", DriverPaths: []string{`E:\viostor\2k22\amd64`}}
	d.Input, d.UserLocale = windowsLocale("fr")
	var b bytes.Buffer
	if err := unattendTmpl.Execute(&b, d); err != nil {
		t.Fatal(err)
	}
	// XML bien formé, caractères spéciaux du mot de passe échappés
	dec := xml.NewDecoder(bytes.NewReader(b.Bytes()))
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("XML invalide : %v\n%s", err, b.String())
			}
			break
		}
	}
	for _, want := range []string{"<Value>Windows Server 2022 SERVERSTANDARD</Value>", "<InputLocale>040c:0000040c</InputLocale>",
		`<Path>E:\viostor\2k22\amd64</Path>`, "<ComputerName>WIN22</ComputerName>", "P@ss&lt;&amp;&gt;&#34;w0rd"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("absent : %s", want)
		}
	}
}
