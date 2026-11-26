CREATE TABLE model_usage (
	workspace VARCHAR NOT NULL, 
	room VARCHAR NOT NULL, 
	slot INTEGER NOT NULL, 
	model VARCHAR NOT NULL, 
	input_tokens INTEGER NOT NULL, 
	output_tokens INTEGER NOT NULL, 
	total_tokens INTEGER NOT NULL, 
	source VARCHAR NOT NULL, 
	updated INTEGER NOT NULL, 
	PRIMARY KEY (workspace, room, slot)
);
CREATE INDEX model_usage_model_updated ON model_usage (model, updated);
