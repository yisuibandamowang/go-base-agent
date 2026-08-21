-- 对齐 Java 消息结束状态与问答关联字段。
ALTER TABLE t_message ADD COLUMN IF NOT EXISTS reply_to_message_id VARCHAR(20);
ALTER TABLE t_message ADD COLUMN IF NOT EXISTS message_status VARCHAR(16) NOT NULL DEFAULT 'NORMAL';
COMMENT ON COLUMN t_message.reply_to_message_id IS '当前助手消息对应的用户消息ID';
COMMENT ON COLUMN t_message.message_status IS '消息结束状态：NORMAL=正常完成，INTERRUPTED=用户中断，REJECTED=限流拒绝';
