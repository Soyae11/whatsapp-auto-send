export type SenderOption = {
    name: string;
    mode: 'single' | 'pool';
    health: string;
    accepting: boolean;
};

export type SendResult = {
    number: string;
    status: 'accepted' | 'rejected';
    id?: string;
    error?: string;
};
