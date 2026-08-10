export type SessionStatus =
    | 'new'
    | 'pairing'
    | 'connected'
    | 'disconnected'
    | 'logged_out'
    | 'unhealthy';

export type SessionHealth = {
    socketConnected: boolean;
    lastConnectedAt: string | null;
    lastSuccessfulSendAt: string | null;
    lastFailedSendAt: string | null;
    consecutiveFailures: number;
    lastErrorCode: string | null;
    reconnectAttempts: number;
    nextReconnectInMs: number | null;
};

export type Session = {
    id: string;
    label: string;
    status: SessionStatus;
    phoneNumber: string | null;
    hasQr: boolean;
    createdAt: string;
    updatedAt: string;
    health: SessionHealth;
};
