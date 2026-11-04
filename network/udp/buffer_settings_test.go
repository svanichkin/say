package udp

import "testing"

func TestBufferSettingsDerivedValues(t *testing.T) {
	defer SetSpeakerBuffer(80, 4)
	defer SetJitterLead(120)

	SetSpeakerBuffer(40, 3)
	if audioSpeakerPeriodFrames != 2 || audioSpeakerPeriods != 3 || audioPlayoutLowFrames != 2 || audioLeadMinFrames != 4 {
		t.Fatalf("40 ms x 3: period %d frames, %d periods, low %d, min lead %d",
			audioSpeakerPeriodFrames, audioSpeakerPeriods, audioPlayoutLowFrames, audioLeadMinFrames)
	}
	if audioEchoRefMaxFrames != 6 || audioEchoRefTargetFrames != 4 {
		t.Fatalf("echo reference limits %d/%d, want 6/4", audioEchoRefMaxFrames, audioEchoRefTargetFrames)
	}
	SetJitterLead(200)
	if audioBufferLeadFrames != 10 || audioQueueTargetFrames != 10 {
		t.Fatalf("lead 200 ms: %d frames, trim target %d", audioBufferLeadFrames, audioQueueTargetFrames)
	}
	// A lead at or below one device period is raised just above it.
	SetSpeakerBuffer(80, 4)
	SetJitterLead(40)
	if audioBufferLeadFrames != audioPlayoutLowFrames+1 || audioLeadMinFrames > audioBufferLeadFrames {
		t.Fatalf("lead 40 ms with an 80 ms period: %d frames (min %d)", audioBufferLeadFrames, audioLeadMinFrames)
	}
}
