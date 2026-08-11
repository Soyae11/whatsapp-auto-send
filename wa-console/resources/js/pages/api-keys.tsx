import { Head, router } from '@inertiajs/react';
import { useEffect, useState } from 'react';
import { revoke, store } from '@/actions/App/Http/Controllers/ApiKeysController';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { useClipboard } from '@/hooks/use-clipboard';
import { apiKeys as apiKeysRoute } from '@/routes';
import type { ApiKey, NewApiKey } from '@/types/api-key';
import type { OwnedSender } from '@/types/sender';

export default function ApiKeys({
    keys,
    senders,
}: {
    keys: ApiKey[];
    senders: OwnedSender[];
}) {
    const [name, setName] = useState('');
    const [project, setProject] = useState('');
    const [environment, setEnvironment] = useState<'live' | 'test'>('test');
    const [selectedSenders, setSelectedSenders] = useState<string[]>([]);
    const [revealed, setRevealed] = useState<NewApiKey | null>(null);
    const [copiedText, copy] = useClipboard();

    useEffect(() => {
        return router.on('flash', (event) => {
            const flash = (event as CustomEvent).detail?.flash;
            const newKey = flash?.newKey as NewApiKey | undefined;

            if (newKey) {
                setRevealed(newKey);
            }
        });
    }, []);

    function toggleSender(name: string) {
        setSelectedSenders((current) =>
            current.includes(name)
                ? current.filter((s) => s !== name)
                : [...current, name],
        );
    }

    function create() {
        if (!name || !project || selectedSenders.length === 0) {
            return;
        }

        router.post(
            store.url(),
            { name, project, environment, senders: selectedSenders },
            {
                onSuccess: () => {
                    setName('');
                    setProject('');
                    setSelectedSenders([]);
                },
            },
        );
    }

    function doRevoke(id: string) {
        if (window.confirm('Revoke this key? Anything using it will stop being able to send.')) {
            router.post(revoke.url({ id }));
        }
    }

    return (
        <>
            <Head title="API Keys" />
            <div className="flex flex-1 flex-col gap-4 p-4">
                <Card>
                    <CardHeader>
                        <CardTitle>New API key</CardTitle>
                    </CardHeader>
                    <CardContent>
                        {senders.length === 0 ? (
                            <p className="text-sm text-muted-foreground">
                                No senders yet. Create one on the Senders page
                                before generating a key.
                            </p>
                        ) : (
                            <div className="flex flex-col gap-4">
                                <div className="flex flex-wrap items-end gap-4">
                                    <div className="flex flex-col gap-2">
                                        <Label htmlFor="key-name">Name</Label>
                                        <Input
                                            id="key-name"
                                            className="w-56"
                                            placeholder="e.g. my-project"
                                            value={name}
                                            onChange={(event) =>
                                                setName(event.target.value)
                                            }
                                        />
                                    </div>

                                    <div className="flex flex-col gap-2">
                                        <Label htmlFor="key-project">
                                            Project
                                        </Label>
                                        <Input
                                            id="key-project"
                                            className="w-56"
                                            placeholder="e.g. marketing-site"
                                            value={project}
                                            onChange={(event) =>
                                                setProject(event.target.value)
                                            }
                                        />
                                    </div>

                                    <div className="flex flex-col gap-2">
                                        <span className="text-sm font-medium">
                                            Environment
                                        </span>
                                        <Select
                                            value={environment}
                                            onValueChange={(value) =>
                                                setEnvironment(
                                                    value as 'live' | 'test',
                                                )
                                            }
                                        >
                                            <SelectTrigger className="w-40">
                                                <SelectValue />
                                            </SelectTrigger>
                                            <SelectContent>
                                                <SelectItem value="test">
                                                    Test
                                                </SelectItem>
                                                <SelectItem value="live">
                                                    Live
                                                </SelectItem>
                                            </SelectContent>
                                        </Select>
                                    </div>
                                </div>

                                <div className="flex flex-col gap-2">
                                    <span className="text-sm font-medium">
                                        Senders this key may use
                                    </span>
                                    <div className="flex flex-wrap gap-4">
                                        {senders.map((sender) => (
                                            <label
                                                key={sender.name}
                                                className="flex items-center gap-2 text-sm"
                                            >
                                                <Checkbox
                                                    checked={selectedSenders.includes(
                                                        sender.name,
                                                    )}
                                                    onCheckedChange={() =>
                                                        toggleSender(
                                                            sender.name,
                                                        )
                                                    }
                                                />
                                                {sender.name}
                                            </label>
                                        ))}
                                    </div>
                                </div>

                                <Button
                                    className="self-start"
                                    onClick={create}
                                    disabled={
                                        !name ||
                                        !project ||
                                        selectedSenders.length === 0
                                    }
                                >
                                    Create key
                                </Button>
                            </div>
                        )}
                    </CardContent>
                </Card>

                <Card>
                    <CardHeader>
                        <CardTitle>Your API keys</CardTitle>
                    </CardHeader>
                    <CardContent>
                        {keys.length === 0 ? (
                            <p className="text-sm text-muted-foreground">
                                No keys yet.
                            </p>
                        ) : (
                            <div className="overflow-x-auto">
                                <table className="w-full text-sm">
                                    <thead>
                                        <tr className="border-b text-left">
                                            <th className="p-2 font-medium">
                                                Name
                                            </th>
                                            <th className="p-2 font-medium">
                                                Project
                                            </th>
                                            <th className="p-2 font-medium">
                                                Key
                                            </th>
                                            <th className="p-2 font-medium">
                                                Senders
                                            </th>
                                            <th className="p-2 font-medium">
                                                Status
                                            </th>
                                            <th className="p-2 font-medium">
                                                Actions
                                            </th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {keys.map((key) => (
                                            <tr
                                                key={key.id}
                                                className="border-b last:border-0"
                                            >
                                                <td className="p-2">
                                                    {key.name}
                                                </td>
                                                <td className="p-2">
                                                    {key.project}
                                                </td>
                                                <td className="p-2 font-mono text-xs">
                                                    wsk_{key.environment}
                                                    _...{key.hint}
                                                </td>
                                                <td className="p-2">
                                                    {key.senders.join(', ')}
                                                </td>
                                                <td className="p-2">
                                                    {key.revoked_at ? (
                                                        <Badge variant="destructive">
                                                            revoked
                                                        </Badge>
                                                    ) : (
                                                        <Badge>active</Badge>
                                                    )}
                                                </td>
                                                <td className="p-2">
                                                    {!key.revoked_at && (
                                                        <Button
                                                            size="sm"
                                                            variant="destructive"
                                                            onClick={() =>
                                                                doRevoke(
                                                                    key.id,
                                                                )
                                                            }
                                                        >
                                                            Revoke
                                                        </Button>
                                                    )}
                                                </td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </table>
                            </div>
                        )}
                    </CardContent>
                </Card>
            </div>

            <Dialog open={revealed !== null} onOpenChange={(open) => !open && setRevealed(null)}>
                <DialogContent>
                    <DialogHeader>
                        <DialogTitle>Key created</DialogTitle>
                        <DialogDescription>
                            Copy this secret now — it will not be shown again.
                        </DialogDescription>
                    </DialogHeader>
                    {revealed && (
                        <div className="flex items-center justify-between gap-2 rounded-md border p-4">
                            <span className="break-all font-mono text-sm">
                                {revealed.secret}
                            </span>
                            <Button
                                size="sm"
                                variant="outline"
                                onClick={() => copy(revealed.secret)}
                            >
                                {copiedText === revealed.secret
                                    ? 'Copied'
                                    : 'Copy'}
                            </Button>
                        </div>
                    )}
                </DialogContent>
            </Dialog>
        </>
    );
}

ApiKeys.layout = {
    breadcrumbs: [
        {
            title: 'API Keys',
            href: apiKeysRoute(),
        },
    ],
};
