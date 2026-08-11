export type ApiKey = {
    id: string;
    name: string;
    project: string;
    environment: 'live' | 'test';
    hint: string;
    senders: string[];
    rate_limit: number;
    last_used_at?: string | null;
    revoked_at?: string | null;
    expires_at?: string | null;
    created_at: string;
};

export type NewApiKey = {
    key: ApiKey;
    secret: string;
};
