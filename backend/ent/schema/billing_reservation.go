package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BillingReservation is the durable, idempotent record for a request's
// authorize/extend/capture/release lifecycle, regardless of funding source.
type BillingReservation struct {
	ent.Schema
}

func (BillingReservation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "billing_reservations"}}
}

func (BillingReservation) Fields() []ent.Field {
	return []ent.Field{
		field.String("request_id").MaxLen(128),
		field.Int64("api_key_id"),
		field.Int64("user_id"),
		field.String("funding_source").MaxLen(20),
		field.Int64("subscription_id").Optional().Nillable(),
		field.Float("authorized_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		field.Float("captured_amount").SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).Default(0),
		field.String("status").MaxLen(20).Default("authorized"),
		field.String("authorization_fingerprint").MaxLen(128),
		field.String("request_fingerprint").MaxLen(128).Default(""),
		field.Time("daily_window_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).Optional().Nillable(),
		field.Time("weekly_window_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).Optional().Nillable(),
		field.Time("monthly_window_start").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).Optional().Nillable(),
		field.String("async_task_id").MaxLen(255).Optional().Nillable(),
		field.JSON("async_metadata", json.RawMessage{}).Optional(),
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (BillingReservation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("request_id", "api_key_id").Unique(),
		index.Fields("status", "expires_at"),
		index.Fields("subscription_id", "status"),
		index.Fields("user_id", "status"),
		index.Fields("async_task_id", "api_key_id").Unique(),
	}
}
