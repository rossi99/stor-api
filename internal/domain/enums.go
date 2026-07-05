package domain

// Enum values mirror the iOS app's Expense.swift / Household.swift definitions
// and the CHECK constraints in the migrations.

type Frequency string

const (
	FrequencyWeekly  Frequency = "weekly"
	FrequencyMonthly Frequency = "monthly"
	FrequencyAnnual  Frequency = "annual"
)

func (f Frequency) Valid() bool {
	switch f {
	case FrequencyWeekly, FrequencyMonthly, FrequencyAnnual:
		return true
	}
	return false
}

type Category string

const (
	CategoryBills         Category = "bills"
	CategorySubscriptions Category = "subscriptions"
	CategorySinkingFunds  Category = "sinkingFunds"
	CategoryGroceries     Category = "groceries"
	CategoryTransport     Category = "transport"
	CategoryEatingOut     Category = "eatingOut"
	CategoryEntertainment Category = "entertainment"
	CategoryHealth        Category = "health"
	CategoryClothing      Category = "clothing"
	CategoryOther         Category = "other"
)

func (c Category) Valid() bool {
	_, ok := categoryBuckets[c]
	return ok
}

type Bucket string

const (
	BucketNeeds   Bucket = "needs"
	BucketWants   Bucket = "wants"
	BucketSavings Bucket = "savings"
)

var categoryBuckets = map[Category]Bucket{
	CategoryBills:         BucketNeeds,
	CategoryGroceries:     BucketNeeds,
	CategoryTransport:     BucketNeeds,
	CategoryHealth:        BucketNeeds,
	CategorySubscriptions: BucketWants,
	CategoryEatingOut:     BucketWants,
	CategoryEntertainment: BucketWants,
	CategoryClothing:      BucketWants,
	CategoryOther:         BucketWants,
	CategorySinkingFunds:  BucketSavings,
}

func (c Category) Bucket() Bucket { return categoryBuckets[c] }

type SplitMethod string

const (
	SplitProportional SplitMethod = "proportional"
	SplitEven         SplitMethod = "even"
)

func (s SplitMethod) Valid() bool { return s == SplitProportional || s == SplitEven }

const (
	ExpenseStatusPending  = "pending"
	ExpenseStatusApproved = "approved"
)
