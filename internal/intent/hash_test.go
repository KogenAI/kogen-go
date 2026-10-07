package intent

import "testing"

func TestExactIntentAndApprovalDigests(t *testing.T) {
	intent := []byte("intent\r\n")
	acceptance := []byte{0xff, '\n', 't'}
	if got, want := IntentSHA256(intent), "fa4c67ed40c42ddd0b60e877bc6f0ca1aa30b07b1559fed338ff5eac6bef3888"; got != want {
		t.Fatalf("IntentSHA256 = %s; want %s", got, want)
	}
	if got, want := ApprovalSHA256(intent, acceptance), "fb969b171bbd242761e60861f4b24deca4de0724af9912ddb833d988b52f58ef"; got != want {
		t.Fatalf("ApprovalSHA256 = %s; want %s", got, want)
	}
	if ApprovalSHA256(intent, []byte("\xff\nt")) == ApprovalSHA256(intent, []byte("\xff\nt\n")) {
		t.Fatal("approval hash normalized acceptance bytes")
	}
	if ApprovalSHA256(intent, acceptance) == ApprovalSHA256([]byte("intent\n"), acceptance) {
		t.Fatal("approval hash normalized Intent bytes")
	}
}
