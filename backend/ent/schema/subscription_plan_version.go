package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SubscriptionPlanVersion is an immutable snapshot of the commercial terms
// published for a subscription plan. Existing subscriptions always retain the
// version they purchased, while future purchases use the plan's published version.
type SubscriptionPlanVersion struct {
	ent.Schema
}

func (SubscriptionPlanVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "subscription_plan_versions"}}
}

func (SubscriptionPlanVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("plan_id"),
		field.Int("version"),
		field.Float("price").SchemaType(map[string]string{dialect.Postgres: "decimal(20,3)"}),
		field.Float("original_price").SchemaType(map[string]string{dialect.Postgres: "decimal(20,3)"}).Optional().Nillable(),
		field.String("currency").MaxLen(3).Default(""),
		field.Int("validity_days"),
		field.String("validity_unit").MaxLen(10).Default("day"),
		field.Float("daily_limit_usd").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Optional().Nillable(),
		field.Float("weekly_limit_usd").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Optional().Nillable(),
		field.Float("monthly_limit_usd").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Optional().Nillable(),
		field.Time("published_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SubscriptionPlanVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("plan", SubscriptionPlan.Type).Ref("versions").Field("plan_id").Unique().Required(),
		edge.To("subscriptions", UserSubscription.Type),
		edge.To("redeem_codes", RedeemCode.Type),
	}
}

func (SubscriptionPlanVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("plan_id", "version").Unique(),
		index.Fields("published_at"),
	}
}
