package lending

// The numbers the running line produces for live_rules_golden_test.go. Never edited to
// make a failing test pass: a change here is a consensus change.

// One day (86,400 s) of block time at 50 percent utilisation on the default curve, a
// 10 percent reserve factor, 43,200 blocks apart (so a block-count basis would give a
// very different answer).
var goldenAccrualDay = map[string]string{
	"borrow index":    "1000260273972602739719563770",
	"supply index":    "1000117123287671232873803697",
	"total borrowed":  "500130136986301369860",
	"total supplied":  "1000130136986301369860",
	"protocol fees":   "13013698630136986",
	"stamp":           "1700086400",
	"stamped block":   "43300",
	"year in seconds": "31536000",
}

// An unstamped market 100 blocks after its last refresh: 100 seconds at one second a block.
var goldenAccrualHundredBlocks = map[string]string{
	"borrow index":   "1000000301243023845763564310",
	"supply index":   "1000000135559360730593603939",
	"total borrowed": "500000150621511922882",
	"protocol fees":  "15062151192288",
	"stamp":          "1700000000",
}
