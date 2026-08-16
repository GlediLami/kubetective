package score

// The weight scale.
//
// Every number the engine adds to a margin comes from this file. Before it
// existed, each analyzer carried its own float constants — thirty-odd values
// clustered on 10/15/20/25/30/40 by shared intuition that was written down
// nowhere. The clustering was not accidental, but nothing recorded what the
// bands meant, so nothing stopped them drifting apart.
//
// Six bands, and what qualifies for each:
//
//	Decisive (40)      The observation that *defines* a failure mode and
//	                   subordinates the symptoms below it. Reserved for
//	                   cross-scope causes: a node under pressure explains every
//	                   pod on it, so node pressure outranks the per-pod memory
//	                   hypothesis it produces. Only one analyzer uses this
//	                   band, and adding a second should require an argument.
//
//	Primary (30)       Direct evidence of the mechanism itself: the container's
//	                   waiting reason, the OOMKill timing, the commit that
//	                   touched the workload. This is the ordinary weight for
//	                   "the thing that went wrong is visible in this
//	                   observation".
//
//	Strong (25)        Evidence that is not the mechanism but is hard to
//	                   explain any other way — a commit landing minutes before
//	                   onset, a GitOps controller reporting Degraded at the
//	                   same time.
//
//	Corroborating (20) An independent source agreeing with the mechanism:
//	                   Prometheus confirming the limit was reached, an event
//	                   message restating a container state. Independence is the
//	                   test — evidence derived from the same observation as a
//	                   Primary term does not also earn Corroborating.
//
//	Supporting (15)    Consistent context that would be surprising if the
//	                   hypothesis were false: a memory limit being set at all,
//	                   a second pressure type on the same node.
//
//	Contextual (10)    Weakly informative. Present, relevant, and nearly always
//	                   also true of the competing hypotheses — restart counts,
//	                   resource requests, captured logs.
//
// Calibration note: these are *margin* contributions, not probabilities. A
// margin becomes a displayed confidence through sigmoid(margin/T), and T is
// only ever fitted against benchmark scenarios the engine gets wrong (see
// adoptionRefusal). Changing a weight therefore changes the ranking directly
// and the confidence only through the sigmoid — which is why the ordering
// between bands matters more than any single value.
//
// Contradicting evidence uses the same bands with Polarity -1. A contradiction
// should be weighted by how strongly it argues *against* the claim, which is
// usually one band below the same evidence arguing for it: OOMKilled contradicts
// a plain crash loop at Corroborating, not Primary, because a crash loop with an
// OOMKill is still a crash loop — just better explained elsewhere.
const (
	WeightDecisive      = 40.0
	WeightPrimary       = 30.0
	WeightStrong        = 25.0
	WeightCorroborating = 20.0
	WeightSupporting    = 15.0
	WeightContextual    = 10.0
)
