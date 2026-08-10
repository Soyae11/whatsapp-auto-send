export type SenderOption = {
    name: string;
    health: string;
    accepting: boolean;
};

export type SendResult = {
    number: string;
    status: 'accepted' | 'rejected';
    id?: string;
    error?: string;
};
