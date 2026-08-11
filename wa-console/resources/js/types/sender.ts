export type SenderMode = 'single' | 'pool';

export type OwnedSender = {
    name: string;
    mode: SenderMode;
    session_id?: string;
};
