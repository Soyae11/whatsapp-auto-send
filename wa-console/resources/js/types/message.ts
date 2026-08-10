export type MessageStatus =
    | 'queued'
    | 'sending'
    | 'sent'
    | 'delivered'
    | 'read'
    | 'cancelled'
    | 'failed';

export type MessageError = {
    code: string;
    detail: string;
    retryable: boolean;
    at: string;
};

export type MessageTimestamps = {
    queued_at?: string;
    sending_at?: string;
    sent_at?: string;
    delivered_at?: string;
    read_at?: string;
    cancelled_at?: string;
    failed_at?: string;
};

export type Message = {
    id: string;
    status: MessageStatus;
    sender: string;
    to: string;
    type: string;
    priority: string;
    dry_run: boolean;
    estimated_send_at?: string;
    reference?: string;
    metadata?: Record<string, string>;
    attempts: number;
    last_error?: MessageError;
    timestamps: MessageTimestamps;
    created_at: string;
    /** Set when this message exists because a pool failed the original over to a different session. */
    failover_of?: string;
    /** Set when this message failed and a pool resent it as a different message. */
    retried_by?: string;
};

export type MessageFilters = {
    sender?: string;
    to?: string;
    status?: string;
    reference?: string;
    created_after?: string;
    created_before?: string;
};
