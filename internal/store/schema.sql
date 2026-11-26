CREATE TABLE conversation (
	id VARCHAR NOT NULL, 
	brief VARCHAR NOT NULL, 
	open INTEGER NOT NULL, 
	round VARCHAR, 
	round_deadline INTEGER, 
	created INTEGER, 
	archived INTEGER DEFAULT 0 NOT NULL, 
	open_invite_hash VARCHAR, 
	title VARCHAR, 
	PRIMARY KEY (id)
);
CREATE UNIQUE INDEX conversation_open_invite_hash ON conversation (open_invite_hash);
CREATE TABLE participant (
	id INTEGER NOT NULL, 
	room VARCHAR NOT NULL, 
	label VARCHAR NOT NULL, 
	role INTEGER NOT NULL, 
	invite_hash VARCHAR, 
	expires INTEGER, 
	token_hash VARCHAR, 
	seen INTEGER, 
	retired INTEGER DEFAULT 0 NOT NULL, 
	model VARCHAR, 
	token_budget INTEGER, 
	PRIMARY KEY (id), 
	UNIQUE (token_hash), 
	FOREIGN KEY(room) REFERENCES conversation (id), 
	UNIQUE (invite_hash), 
	UNIQUE (room, label)
);
CREATE TABLE assignment (
	id VARCHAR NOT NULL, 
	slot INTEGER NOT NULL, 
	room VARCHAR NOT NULL, 
	plugin VARCHAR NOT NULL, 
	invite_hash VARCHAR NOT NULL, 
	state VARCHAR NOT NULL, 
	deadline INTEGER NOT NULL, 
	round VARCHAR, 
	exit_code INTEGER, 
	mcp_task_hash VARCHAR, 
	cancel_requested INTEGER DEFAULT 0 NOT NULL, 
	created INTEGER, 
	updated INTEGER, 
	PRIMARY KEY (id), 
	FOREIGN KEY(slot) REFERENCES participant (id), 
	UNIQUE (slot), 
	FOREIGN KEY(room) REFERENCES conversation (id)
);
CREATE INDEX assignment_active ON assignment (state, deadline);
CREATE UNIQUE INDEX assignment_mcp_task_hash ON assignment (mcp_task_hash);
CREATE TABLE message (
	id INTEGER NOT NULL, 
	room VARCHAR NOT NULL, 
	author INTEGER NOT NULL, 
	body VARCHAR NOT NULL, 
	created INTEGER NOT NULL, 
	kind VARCHAR, 
	recipient INTEGER, 
	reply_to INTEGER, 
	due_minutes INTEGER, 
	PRIMARY KEY (id), 
	FOREIGN KEY(reply_to) REFERENCES message (id), 
	FOREIGN KEY(author) REFERENCES participant (id), 
	FOREIGN KEY(room) REFERENCES conversation (id), 
	FOREIGN KEY(recipient) REFERENCES participant (id)
);
CREATE INDEX message_author_recent ON message (author, created);
CREATE INDEX message_room_kind ON message (room, kind, author, reply_to);
CREATE INDEX message_room_cursor ON message (room, id);
CREATE TABLE participant_usage (
	slot INTEGER NOT NULL, 
	room VARCHAR NOT NULL, 
	estimated_input INTEGER DEFAULT 0 NOT NULL, 
	estimated_output INTEGER DEFAULT 0 NOT NULL, 
	reported_input INTEGER, 
	reported_output INTEGER, 
	source VARCHAR DEFAULT 'estimated' NOT NULL, 
	updated INTEGER NOT NULL, 
	PRIMARY KEY (slot), 
	FOREIGN KEY(slot) REFERENCES participant (id), 
	FOREIGN KEY(room) REFERENCES conversation (id)
);
CREATE INDEX participant_usage_room ON participant_usage (room);
CREATE TABLE proposal (
	id VARCHAR NOT NULL, 
	room VARCHAR NOT NULL, 
	author INTEGER NOT NULL, 
	body VARCHAR NOT NULL, 
	PRIMARY KEY (id), 
	UNIQUE (room, author), 
	FOREIGN KEY(author) REFERENCES participant (id), 
	FOREIGN KEY(room) REFERENCES conversation (id)
);
CREATE TABLE submission (
	author INTEGER NOT NULL, 
	"key" VARCHAR NOT NULL, 
	message_id INTEGER, 
	proposal_id VARCHAR, 
	PRIMARY KEY (author, "key"), 
	FOREIGN KEY(message_id) REFERENCES message (id), 
	FOREIGN KEY(author) REFERENCES participant (id), 
	CONSTRAINT submission_target CHECK ((message_id IS NULL) != (proposal_id IS NULL)), 
	CONSTRAINT submission_proposal_fk FOREIGN KEY(proposal_id) REFERENCES proposal (id)
);
CREATE VIRTUAL TABLE conversation_search USING fts5(body, room UNINDEXED, message_id UNINDEXED);
CREATE TRIGGER conversation_search_brief AFTER INSERT ON conversation BEGIN INSERT INTO conversation_search (body, room, message_id) VALUES (new.brief, new.id, NULL); END;
CREATE TRIGGER conversation_search_message AFTER INSERT ON message BEGIN INSERT INTO conversation_search (body, room, message_id) VALUES (new.body, new.room, new.id); END;
