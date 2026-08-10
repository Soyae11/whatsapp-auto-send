export type PoolMember = {
    session_id: string;
    is_main: boolean;
    disqualified: boolean;
    rank: number;
};

export type PoolSession = {
    id: string;
    label: string;
    phoneNumber: string | null;
};
