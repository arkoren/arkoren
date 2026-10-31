// Package semconv holds the OpenTelemetry semantic-convention names that
// Arkoren instrumentation emits, pinned to one semconv version so every
// component and driver agrees on spelling. Cache has no semantic convention,
// so its names live under the arkoren.cache namespace.
//
// The constants are plain strings: core never imports OpenTelemetry.
// Instrumentation uses these names with the core telemetry API, and native
// drivers map the same strings without introducing a dependency on an SDK here.
//
//	attribute := telemetry.String(semconv.HTTPRequestMethod, "GET")
//	span.SetAttributes(attribute)
package semconv

// Version is the semantic-convention release the names below follow.
// GenAI names use their separately pinned GenAIVersion instead.
//
// Example: fmt.Println(semconv.Version)
const Version = "1.37.0"

// Resource attributes describing the emitting service.
const (
	// ServiceName is the logical name of the service.
	// Example: resource.Attrs = append(resource.Attrs, telemetry.String(semconv.ServiceName, "billing"))
	ServiceName = "service.name"

	// ServiceVersion is the version of the service.
	// Example: resource.Attrs = append(resource.Attrs, telemetry.String(semconv.ServiceVersion, "1.2.3"))
	ServiceVersion = "service.version"

	// DeploymentEnvironmentName is the deployment environment, such as
	// production.
	// Example: span.SetAttributes(telemetry.String(semconv.DeploymentEnvironmentName, "production"))
	DeploymentEnvironmentName = "deployment.environment.name"
)

// Error and exception attributes.
const (
	// ErrorType classifies a failed operation with a low-cardinality value:
	// a status code or a Go error type name, never the error text.
	// Example: span.SetAttributes(telemetry.String(semconv.ErrorType, "context.Canceled"))
	ErrorType = "error.type"

	// ExceptionType is the Go type name of a recorded error.
	// Example: span.AddEvent(semconv.ExceptionEventName, telemetry.String(semconv.ExceptionType, "*errors.errorString"))
	ExceptionType = "exception.type"

	// ExceptionMessage is the text of a recorded error.
	// Use only for explicitly approved diagnostics; automatic facades omit it.
	// Example: span.AddEvent(semconv.ExceptionEventName, telemetry.String(semconv.ExceptionMessage, "operation failed"))
	ExceptionMessage = "exception.message"

	// ExceptionEventName is the name of the span event that records an error.
	// Example: span.AddEvent(semconv.ExceptionEventName, telemetry.String(semconv.ExceptionType, "*errors.errorString"))
	ExceptionEventName = "exception"
)

// HTTP attributes and metrics.
const (
	// HTTPRequestMethod is the HTTP method of a request.
	// Example: span.SetAttributes(telemetry.String(semconv.HTTPRequestMethod, "GET"))
	HTTPRequestMethod = "http.request.method"

	// HTTPRequestMethodOriginal is the method as received when it is not
	// a known method.
	// Example: span.SetAttributes(telemetry.String(semconv.HTTPRequestMethodOriginal, "CUSTOM"))
	HTTPRequestMethodOriginal = "http.request.method_original"

	// HTTPResponseStatusCode is the HTTP status code of a response.
	// Example: span.SetAttributes(telemetry.Int(semconv.HTTPResponseStatusCode, http.StatusOK))
	HTTPResponseStatusCode = "http.response.status_code"

	// HTTPRoute is the matched route template, never the raw path.
	// Example: span.SetAttributes(telemetry.String(semconv.HTTPRoute, "/orders/{id}"))
	HTTPRoute = "http.route"

	// HTTPRequestResendCount is the number of times a client resent a
	// request.
	// Example: span.SetAttributes(telemetry.Int(semconv.HTTPRequestResendCount, 1))
	HTTPRequestResendCount = "http.request.resend_count"

	// URLScheme is the URI scheme, http or https.
	// Example: span.SetAttributes(telemetry.String(semconv.URLScheme, "https"))
	URLScheme = "url.scheme"

	// NetworkProtocolName is the application protocol name, such as http.
	// Example: span.SetAttributes(telemetry.String(semconv.NetworkProtocolName, "http"))
	NetworkProtocolName = "network.protocol.name"

	// NetworkProtocolVersion is the application protocol version, such as
	// 1.1.
	// Example: span.SetAttributes(telemetry.String(semconv.NetworkProtocolVersion, "2"))
	NetworkProtocolVersion = "network.protocol.version"

	// ServerAddress is the host name of the server a client talks to.
	// Example: span.SetAttributes(telemetry.String(semconv.ServerAddress, "api.example.com"))
	ServerAddress = "server.address"

	// ServerPort is the port of the server a client talks to.
	// Example: span.SetAttributes(telemetry.Int(semconv.ServerPort, 443))
	ServerPort = "server.port"

	// HTTPServerRequestDuration is the server request duration histogram, in
	// seconds.
	//
	//	histogram := meter.Histogram(semconv.HTTPServerRequestDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.2)
	HTTPServerRequestDuration = "http.server.request.duration"

	// HTTPServerActiveRequests is the in-flight server request up-down
	// counter.
	// Example: active := meter.UpDownCounter(semconv.HTTPServerActiveRequests); active.Add(ctx, 1)
	HTTPServerActiveRequests = "http.server.active_requests"

	// HTTPClientRequestDuration is the client request duration histogram, in
	// seconds.
	//
	//	histogram := meter.Histogram(semconv.HTTPClientRequestDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.2)
	HTTPClientRequestDuration = "http.client.request.duration"
)

// Database attributes and metrics.
const (
	// DBSystemName identifies the database product, such as postgresql.
	// Example: span.SetAttributes(telemetry.String(semconv.DBSystemName, "postgresql"))
	DBSystemName = "db.system.name"

	// DBNamespace is the database name.
	// Example: span.SetAttributes(telemetry.String(semconv.DBNamespace, "billing"))
	DBNamespace = "db.namespace"

	// DBOperationName is the operation, such as SELECT.
	// Example: span.SetAttributes(telemetry.String(semconv.DBOperationName, "SELECT"))
	DBOperationName = "db.operation.name"

	// DBCollectionName is the table the operation targets.
	// Example: span.SetAttributes(telemetry.String(semconv.DBCollectionName, "orders"))
	DBCollectionName = "db.collection.name"

	// DBQueryText is the parameterized query text, with placeholders only.
	// Example: span.SetAttributes(telemetry.String(semconv.DBQueryText, "SELECT id FROM orders WHERE id = ?"))
	DBQueryText = "db.query.text"

	// DBQuerySummary is a low-cardinality summary of a query.
	// Example: span.SetAttributes(telemetry.String(semconv.DBQuerySummary, "SELECT orders"))
	DBQuerySummary = "db.query.summary"

	// DBResponseStatusCode is the database-specific status code of a
	// failure.
	// Example: span.SetAttributes(telemetry.String(semconv.DBResponseStatusCode, "23505"))
	DBResponseStatusCode = "db.response.status_code"

	// DBClientOperationDuration is the client operation duration histogram,
	// in seconds.
	//
	//	histogram := meter.Histogram(semconv.DBClientOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.02)
	DBClientOperationDuration = "db.client.operation.duration"
)

// Messaging attributes and metrics.
const (
	// MessagingSystem identifies the messaging product, such as rabbitmq.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingSystem, "rabbitmq"))
	MessagingSystem = "messaging.system"

	// MessagingOperationName is the system-specific operation name.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationName, "send"))
	MessagingOperationName = "messaging.operation.name"

	// MessagingOperationType is the operation type, one of the
	// MessagingOperation values.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationType, semconv.MessagingOperationSend))
	MessagingOperationType = "messaging.operation.type"

	// MessagingDestinationName is the topic, queue, or subject name.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingDestinationName, "billing"))
	MessagingDestinationName = "messaging.destination.name"

	// MessagingDestinationSubscriptionName is the subscription a consumer
	// reads from.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingDestinationSubscriptionName, "billing-worker"))
	MessagingDestinationSubscriptionName = "messaging.destination.subscription.name"

	// MessagingConsumerGroupName is the consumer group name.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingConsumerGroupName, "billing-workers"))
	MessagingConsumerGroupName = "messaging.consumer.group.name"

	// MessagingMessageID is the broker-assigned or application message ID.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingMessageID, "message-42"))
	MessagingMessageID = "messaging.message.id"

	// MessagingClientOperationDuration is the client operation duration
	// histogram, in seconds.
	//
	//	histogram := meter.Histogram(semconv.MessagingClientOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.02)
	MessagingClientOperationDuration = "messaging.client.operation.duration"

	// MessagingProcessDuration is the consumer processing duration
	// histogram, in seconds.
	//
	//	histogram := meter.Histogram(semconv.MessagingProcessDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.02)
	MessagingProcessDuration = "messaging.process.duration"

	// MessagingClientSentMessages counts messages produced.
	// Example: sent := meter.Counter(semconv.MessagingClientSentMessages); sent.Add(ctx, 1)
	MessagingClientSentMessages = "messaging.client.sent.messages"

	// MessagingClientConsumedMessages counts messages consumed.
	// Example: consumed := meter.Counter(semconv.MessagingClientConsumedMessages); consumed.Add(ctx, 1)
	MessagingClientConsumedMessages = "messaging.client.consumed.messages"
)

// Values of MessagingOperationType.
const (
	// MessagingOperationCreate creates a message.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationType, semconv.MessagingOperationCreate))
	MessagingOperationCreate = "create"

	// MessagingOperationSend sends a message to a destination.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationType, semconv.MessagingOperationSend))
	MessagingOperationSend = "send"

	// MessagingOperationReceive receives a message from a destination.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationType, semconv.MessagingOperationReceive))
	MessagingOperationReceive = "receive"

	// MessagingOperationProcess processes a received message.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationType, semconv.MessagingOperationProcess))
	MessagingOperationProcess = "process"

	// MessagingOperationSettle acknowledges or rejects a message.
	// Example: span.SetAttributes(telemetry.String(semconv.MessagingOperationType, semconv.MessagingOperationSettle))
	MessagingOperationSettle = "settle"
)

// Queue attributes. Messaging has a semantic convention, but job names and
// attempts do not, so these names are Arkoren's own.
const (
	// QueueJob is the application job name of a queue message.
	// Example: span.SetAttributes(telemetry.String(semconv.QueueJob, "send-receipt"))
	QueueJob = "arkoren.queue.job"

	// QueueAttempt is the delivery attempt number of a queue message.
	// Example: span.SetAttributes(telemetry.Int(semconv.QueueAttempt, 1))
	QueueAttempt = "arkoren.queue.attempt"
)

// MessagingSystemOther is the messaging.system value for a driver that does
// not name its system.
// Example: span.SetAttributes(telemetry.String(semconv.MessagingSystem, semconv.MessagingSystemOther))
const MessagingSystemOther = "_OTHER"

// Cache attributes and metrics. Cache has no semantic convention, so these
// names are Arkoren's own.
const (
	// CacheOperation is the cache operation, such as get or put.
	// Example: span.SetAttributes(telemetry.String(semconv.CacheOperation, "get"))
	CacheOperation = "arkoren.cache.operation"

	// CacheHit reports whether a read found its key.
	// Example: span.SetAttributes(telemetry.Bool(semconv.CacheHit, true))
	CacheHit = "arkoren.cache.hit"

	// CacheStore names the cache store.
	// Example: span.SetAttributes(telemetry.String(semconv.CacheStore, "sessions"))
	CacheStore = "arkoren.cache.store"

	// CacheOperationDuration is the cache operation duration histogram, in
	// seconds.
	//
	//	histogram := meter.Histogram(semconv.CacheOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.01)
	CacheOperationDuration = "arkoren.cache.operation.duration"

	// CacheOperations counts cache operations.
	// Example: operations := meter.Counter(semconv.CacheOperations); operations.Add(ctx, 1)
	CacheOperations = "arkoren.cache.operations"
)

// Search attributes and metrics. Search has no semantic convention, so these
// names are Arkoren's own.
const (
	// SearchOperation is the search operation, such as put or search.
	// Example: span.SetAttributes(telemetry.String(semconv.SearchOperation, "search"))
	SearchOperation = "arkoren.search.operation"

	// SearchName names the search engine facade.
	// Example: span.SetAttributes(telemetry.String(semconv.SearchName, "catalog"))
	SearchName = "arkoren.search.name"

	// SearchIndex is the index name. Index names are chosen by developers
	// and bounded, so they are safe as metric labels.
	// Example: span.SetAttributes(telemetry.String(semconv.SearchIndex, "products"))
	SearchIndex = "arkoren.search.index"

	// SearchHits is the number of hits a search returned on its page.
	// Example: span.SetAttributes(telemetry.Int(semconv.SearchHits, 10))
	SearchHits = "arkoren.search.hits"

	// SearchOperationDuration is the search operation duration histogram,
	// in seconds.
	//
	//	histogram := meter.Histogram(semconv.SearchOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.1)
	SearchOperationDuration = "arkoren.search.operation.duration"

	// SearchOperations counts search operations.
	// Example: operations := meter.Counter(semconv.SearchOperations); operations.Add(ctx, 1)
	SearchOperations = "arkoren.search.operations"
)

// Feature flag attributes and metrics. The attributes follow OpenTelemetry's
// feature_flag conventions (release candidate); the metrics have no
// convention, so their names are Arkoren's own.
const (
	// FeatureFlagKey is the flag key. Keys are chosen by developers and
	// bounded, so they are safe as metric labels.
	// Example: span.SetAttributes(telemetry.String(semconv.FeatureFlagKey, "checkout-redesign"))
	FeatureFlagKey = "feature_flag.key"

	// FeatureFlagResultReason is how the value was chosen, such as static or
	// targeting_match.
	// Example: span.SetAttributes(telemetry.String(semconv.FeatureFlagResultReason, "static"))
	FeatureFlagResultReason = "feature_flag.result.reason"

	// FeatureFlagResultVariant is the vendor's name for the evaluated value.
	// It is a span attribute only: variants are vendor data, not bounded by
	// the application.
	// Example: span.SetAttributes(telemetry.String(semconv.FeatureFlagResultVariant, "control"))
	FeatureFlagResultVariant = "feature_flag.result.variant"

	// FeatureFlagProviderName names the feature flag provider.
	// Example: span.SetAttributes(telemetry.String(semconv.FeatureFlagProviderName, "memory"))
	FeatureFlagProviderName = "feature_flag.provider.name"

	// FeatureEvaluationDuration is the flag evaluation duration histogram, in
	// seconds.
	//
	//	histogram := meter.Histogram(semconv.FeatureEvaluationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.001)
	FeatureEvaluationDuration = "arkoren.feature.evaluation.duration"

	// FeatureEvaluations counts flag evaluations.
	// Example: evaluations := meter.Counter(semconv.FeatureEvaluations); evaluations.Add(ctx, 1)
	FeatureEvaluations = "arkoren.feature.evaluations"
)

// Schedule attributes and metrics. OpenTelemetry's faas.cron and
// faas.trigger attributes are experimental and describe functions-as-a-service
// invocations, so these names are Arkoren's own.
const (
	// ScheduleEntry is the name of the scheduled entry or task, a developer
	// chosen identifier.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleEntry, "daily-report"))
	ScheduleEntry = "arkoren.schedule.entry"

	// ScheduleKind is what ran: ScheduleKindEntry or ScheduleKindTask.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleKind, semconv.ScheduleKindEntry))
	ScheduleKind = "arkoren.schedule.kind"

	// ScheduleOutcome is the result of a run: ScheduleOutcomeOK,
	// ScheduleOutcomeSkipped or ScheduleOutcomeFailed.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeOK))
	ScheduleOutcome = "arkoren.schedule.outcome"

	// ScheduleRunCount counts scheduled runs.
	// Example: runs := meter.Counter(semconv.ScheduleRunCount); runs.Add(ctx, 1)
	ScheduleRunCount = "arkoren.schedule.run.count"

	// ScheduleRunDuration is the scheduled run duration histogram, in
	// seconds.
	// Example: histogram := meter.Histogram(semconv.ScheduleRunDuration, telemetry.Unit("s")); histogram.Record(ctx, 1.5)
	ScheduleRunDuration = "arkoren.schedule.run.duration"
)

// Values of ScheduleKind and ScheduleOutcome.
const (
	// ScheduleKindEntry is a recurring entry.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleKind, semconv.ScheduleKindEntry))
	ScheduleKindEntry = "entry"

	// ScheduleKindTask is a one-off task deferred with At or After.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleKind, semconv.ScheduleKindTask))
	ScheduleKindTask = "task"

	// ScheduleOutcomeOK means the run returned no error.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeOK))
	ScheduleOutcomeOK = "ok"

	// ScheduleOutcomeSkipped means the run returned ErrSkipped.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeSkipped))
	ScheduleOutcomeSkipped = "skipped"

	// ScheduleOutcomeFailed means the run returned an error or panicked.
	// Example: span.SetAttributes(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeFailed))
	ScheduleOutcomeFailed = "failed"
)

// Storage attributes and metrics. Object storage has no semantic convention,
// so these names are Arkoren's own.
const (
	// StorageOperation is the storage operation, such as put or open.
	// Example: span.SetAttributes(telemetry.String(semconv.StorageOperation, "put"))
	StorageOperation = "arkoren.storage.operation"

	// StorageStore names the storage store.
	// Example: span.SetAttributes(telemetry.String(semconv.StorageStore, "receipts"))
	StorageStore = "arkoren.storage.store"

	// StorageObjectSize is the size in bytes of a stored object. It is a
	// span attribute only.
	// Example: span.SetAttributes(telemetry.Int64(semconv.StorageObjectSize, 1024))
	StorageObjectSize = "arkoren.storage.object.size"

	// StorageOperations counts storage operations.
	// Example: operations := meter.Counter(semconv.StorageOperations); operations.Add(ctx, 1)
	StorageOperations = "arkoren.storage.operations"

	// StorageOperationDuration is the storage operation duration histogram,
	// in seconds.
	//
	//	histogram := meter.Histogram(semconv.StorageOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.1)
	StorageOperationDuration = "arkoren.storage.operation.duration"
)

// Mail attributes and metrics. Mail has no semantic convention, so these
// names are Arkoren's own.
const (
	// MailOperation is the mail operation, currently always send.
	// Example: span.SetAttributes(telemetry.String(semconv.MailOperation, "send"))
	MailOperation = "arkoren.mail.operation"

	// MailRecipients is the number of to, cc and bcc recipients of a
	// message. It is a span attribute only.
	// Example: span.SetAttributes(telemetry.Int(semconv.MailRecipients, 1))
	MailRecipients = "arkoren.mail.recipients"

	// MailAttachments is the number of attachments of a message. It is a span
	// attribute only.
	// Example: span.SetAttributes(telemetry.Int(semconv.MailAttachments, 2))
	MailAttachments = "arkoren.mail.attachments"

	// MailOperations counts mail operations.
	// Example: operations := meter.Counter(semconv.MailOperations); operations.Add(ctx, 1)
	MailOperations = "arkoren.mail.operations"

	// MailOperationDuration is the mail operation duration histogram, in
	// seconds.
	//
	//	histogram := meter.Histogram(semconv.MailOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.2)
	MailOperationDuration = "arkoren.mail.operation.duration"
)

// Push attributes and metrics are Arkoren's own bounded operation names.
// Payloads, recipients, credentials, and receipt IDs are never labels.
const (
	// PushOperation is the static submission operation name, currently send.
	// It never contains provider or application input.
	//
	// Example:
	//
	//	attr := telemetry.String(semconv.PushOperation, "send")
	//	_ = attr
	PushOperation = "arkoren.push.operation"

	// PushOperations counts submissions after portable validation succeeds.
	// It shares bounded operation and error-class labels with the histogram.
	//
	// Example:
	//
	//	counter := meter.Counter(semconv.PushOperations)
	//	_ = counter
	PushOperations = "arkoren.push.operations"

	// PushOperationDuration records provider submission duration in seconds.
	// It does not measure device delivery or notification display latency.
	//
	// Example:
	//
	//	histogram := meter.Histogram(semconv.PushOperationDuration)
	//	_ = histogram
	PushOperationDuration = "arkoren.push.operation.duration"
)

// Payment attributes and metrics have no standard semantic convention; these
// names are Arkoren's own. Resource IDs and payment data are never labels.
const (
	// PaymentOperation is the portable operation name, such as create_checkout.
	// Its values are static, never provider input or resource identifiers.
	//
	// Example: attr := telemetry.String(semconv.PaymentOperation, "get_payment"); _ = attr
	PaymentOperation = "arkoren.payment.operation"

	// PaymentOperations counts provider operations after portable validation.
	// Failed validation does not create a provider-operation measurement.
	//
	// Example: counter := meter.Counter(semconv.PaymentOperations); _ = counter
	PaymentOperations = "arkoren.payment.operations"

	// PaymentOperationDuration measures provider operation duration in seconds.
	// It shares low-cardinality operation and error-class labels with the count.
	//
	// Example: histogram := meter.Histogram(semconv.PaymentOperationDuration); _ = histogram
	PaymentOperationDuration = "arkoren.payment.operation.duration"
)

// GenAIVersion is the release of the OpenTelemetry generative AI semantic
// conventions (schema gen-ai/1.42.0) that the gen_ai.* names below follow.
// The conventions are still marked Development upstream, so a rename is a
// change to this file only.
// Example: fmt.Println(semconv.GenAIVersion)
const GenAIVersion = "1.42.0"

// Generative AI attributes and metrics, following the gen-ai semantic
// conventions at [GenAIVersion].
const (
	// GenAIProviderName identifies the model provider, such as openai.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIProviderName, "openai"))
	GenAIProviderName = "gen_ai.provider.name"

	// GenAIOperationName is the operation, one of the GenAIOperation values.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIOperationName, semconv.GenAIOperationChat))
	GenAIOperationName = "gen_ai.operation.name"

	// GenAIRequestModel is the model ID the request asked for.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIRequestModel, "example-model"))
	GenAIRequestModel = "gen_ai.request.model"

	// GenAIResponseModel is the model that answered, as resolved by the
	// provider.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIResponseModel, "example-model"))
	GenAIResponseModel = "gen_ai.response.model"

	// GenAIResponseID is the provider's response ID.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIResponseID, "response-42"))
	GenAIResponseID = "gen_ai.response.id"

	// GenAIResponseFinishReasons lists why generation stopped.
	// Example: span.SetAttributes(telemetry.Strings(semconv.GenAIResponseFinishReasons, []string{"stop"}))
	GenAIResponseFinishReasons = "gen_ai.response.finish_reasons"

	// GenAIUsageInputTokens counts the input tokens of a response.
	// Example: span.SetAttributes(telemetry.Int(semconv.GenAIUsageInputTokens, 120))
	GenAIUsageInputTokens = "gen_ai.usage.input_tokens"

	// GenAIUsageOutputTokens counts the output tokens of a response.
	// Example: span.SetAttributes(telemetry.Int(semconv.GenAIUsageOutputTokens, 24))
	GenAIUsageOutputTokens = "gen_ai.usage.output_tokens"

	// GenAIUsageCacheReadInputTokens counts input tokens served from the
	// provider's prompt cache.
	// Example: span.SetAttributes(telemetry.Int(semconv.GenAIUsageCacheReadInputTokens, 80))
	GenAIUsageCacheReadInputTokens = "gen_ai.usage.cache_read.input_tokens"

	// GenAIUsageCacheCreationInputTokens counts input tokens written to the
	// provider's prompt cache.
	// Example: span.SetAttributes(telemetry.Int(semconv.GenAIUsageCacheCreationInputTokens, 40))
	GenAIUsageCacheCreationInputTokens = "gen_ai.usage.cache_creation.input_tokens"

	// GenAIAgentName is the name of the agent being invoked.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIAgentName, "receipt-assistant"))
	GenAIAgentName = "gen_ai.agent.name"

	// GenAIToolName is the name of the tool being executed.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIToolName, "lookup-order"))
	GenAIToolName = "gen_ai.tool.name"

	// GenAITokenType is the kind of token counted by a
	// [GenAIClientTokenUsage] point, input or output.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAITokenType, semconv.GenAITokenTypeInput))
	GenAITokenType = "gen_ai.token.type"

	// GenAIClientTokenUsage is the token usage histogram, in tokens.
	//
	//	tokens := meter.Histogram(semconv.GenAIClientTokenUsage, telemetry.Unit("{token}"))
	//	tokens.Record(ctx, 120)
	GenAIClientTokenUsage = "gen_ai.client.token.usage"

	// GenAIClientOperationDuration is the client operation duration
	// histogram, in seconds.
	//
	//	histogram := meter.Histogram(semconv.GenAIClientOperationDuration, telemetry.Unit("s"))
	//	histogram.Record(ctx, 0.2)
	GenAIClientOperationDuration = "gen_ai.client.operation.duration"
)

// Values of GenAIOperationName.
const (
	// GenAIOperationChat is a model turn.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIOperationName, semconv.GenAIOperationChat))
	GenAIOperationChat = "chat"

	// GenAIOperationEmbeddings is an embeddings request.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIOperationName, semconv.GenAIOperationEmbeddings))
	GenAIOperationEmbeddings = "embeddings"

	// GenAIOperationInvokeAgent is one agent run.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIOperationName, semconv.GenAIOperationInvokeAgent))
	GenAIOperationInvokeAgent = "invoke_agent"

	// GenAIOperationExecuteTool is one tool call.
	// Example: span.SetAttributes(telemetry.String(semconv.GenAIOperationName, semconv.GenAIOperationExecuteTool))
	GenAIOperationExecuteTool = "execute_tool"
)

// Values of GenAITokenType.
const (
	// GenAITokenTypeInput counts input tokens.
	// Example: tokens.Record(ctx, 120, telemetry.String(semconv.GenAITokenType, semconv.GenAITokenTypeInput))
	GenAITokenTypeInput = "input"

	// GenAITokenTypeOutput counts output tokens.
	// Example: tokens.Record(ctx, 24, telemetry.String(semconv.GenAITokenType, semconv.GenAITokenTypeOutput))
	GenAITokenTypeOutput = "output"
)

// GenAIProviderOther is the gen_ai.provider.name value for a facade whose
// provider was not named.
// Example: span.SetAttributes(telemetry.String(semconv.GenAIProviderName, semconv.GenAIProviderOther))
const GenAIProviderOther = "_OTHER"

// LLM attributes without a gen-ai name. They are span attributes only.
const (
	// LLMUsageReasoningTokens counts output tokens spent on reasoning.
	// Example: span.SetAttributes(telemetry.Int(semconv.LLMUsageReasoningTokens, 8))
	LLMUsageReasoningTokens = "arkoren.llm.usage.reasoning_tokens"

	// LLMAgentSteps is the number of model turns of one agent run.
	// Example: span.SetAttributes(telemetry.Int(semconv.LLMAgentSteps, 3))
	LLMAgentSteps = "arkoren.llm.agent.steps"

	// LLMEmbeddingInputs is the number of texts in an embeddings request.
	// Example: span.SetAttributes(telemetry.Int(semconv.LLMEmbeddingInputs, 2))
	LLMEmbeddingInputs = "arkoren.llm.embedding.inputs"
)
