export namespace main {
	
	export class AlgorithmOption {
	    id: string;
	    available: boolean;
	    substitute: boolean;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new AlgorithmOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.available = source["available"];
	        this.substitute = source["substitute"];
	        this.note = source["note"];
	    }
	}
	export class Option {
	    id: string;
	    title: string;
	    warning: string;
	
	    static createFrom(source: any = {}) {
	        return new Option(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.warning = source["warning"];
	    }
	}
	export class ChatOptions {
	    algorithms: AlgorithmOption[];
	    keyBits: number[];
	    cipherModes: Option[];
	    paddings: Option[];
	
	    static createFrom(source: any = {}) {
	        return new ChatOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.algorithms = this.convertValues(source["algorithms"], AlgorithmOption);
	        this.keyBits = source["keyBits"];
	        this.cipherModes = this.convertValues(source["cipherModes"], Option);
	        this.paddings = this.convertValues(source["paddings"], Option);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ChatView {
	    id: string;
	    peer: string;
	    creator: string;
	    algorithm: string;
	    keyBits: number;
	    cipherMode: string;
	    padding: string;
	    createdAt: number;
	    selfConnected: boolean;
	    peerConnected: boolean;
	    keyState: string;
	    algorithmNote: string;
	    unread: number;
	    receiveState: string;
	    receiveReason: string;
	    canSend: boolean;
	    sendBlock: string;
	
	    static createFrom(source: any = {}) {
	        return new ChatView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.peer = source["peer"];
	        this.creator = source["creator"];
	        this.algorithm = source["algorithm"];
	        this.keyBits = source["keyBits"];
	        this.cipherMode = source["cipherMode"];
	        this.padding = source["padding"];
	        this.createdAt = source["createdAt"];
	        this.selfConnected = source["selfConnected"];
	        this.peerConnected = source["peerConnected"];
	        this.keyState = source["keyState"];
	        this.algorithmNote = source["algorithmNote"];
	        this.unread = source["unread"];
	        this.receiveState = source["receiveState"];
	        this.receiveReason = source["receiveReason"];
	        this.canSend = source["canSend"];
	        this.sendBlock = source["sendBlock"];
	    }
	}
	export class ClosedChatView {
	    id: string;
	    peer: string;
	    algorithm: string;
	    keyBits: number;
	    cipherMode: string;
	    padding: string;
	    createdAt: number;
	    closedAt: number;
	    messageCount: number;
	
	    static createFrom(source: any = {}) {
	        return new ClosedChatView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.peer = source["peer"];
	        this.algorithm = source["algorithm"];
	        this.keyBits = source["keyBits"];
	        this.cipherMode = source["cipherMode"];
	        this.padding = source["padding"];
	        this.createdAt = source["createdAt"];
	        this.closedAt = source["closedAt"];
	        this.messageCount = source["messageCount"];
	    }
	}
	export class ContactResult {
	    becameContact: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ContactResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.becameContact = source["becameContact"];
	    }
	}
	export class CreateChatRequest {
	    peer: string;
	    algorithm: string;
	    keyBits: number;
	    cipherMode: string;
	    padding: string;
	
	    static createFrom(source: any = {}) {
	        return new CreateChatRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peer = source["peer"];
	        this.algorithm = source["algorithm"];
	        this.keyBits = source["keyBits"];
	        this.cipherMode = source["cipherMode"];
	        this.padding = source["padding"];
	    }
	}
	export class MessageError {
	    code: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new MessageError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	    }
	}
	export class Progress {
	    done: number;
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new Progress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.done = source["done"];
	        this.total = source["total"];
	    }
	}
	export class MessageView {
	    seq: number;
	    chatId: string;
	    direction: string;
	    kind: string;
	    createdAt: number;
	    state: string;
	    text: string;
	    fileName: string;
	    size: number;
	    mediaUrl: string;
	    error?: MessageError;
	    encrypt?: Progress;
	    send?: Progress;
	    receive?: Progress;
	    decrypt?: Progress;
	    cancellable: boolean;
	    retryable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MessageView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.seq = source["seq"];
	        this.chatId = source["chatId"];
	        this.direction = source["direction"];
	        this.kind = source["kind"];
	        this.createdAt = source["createdAt"];
	        this.state = source["state"];
	        this.text = source["text"];
	        this.fileName = source["fileName"];
	        this.size = source["size"];
	        this.mediaUrl = source["mediaUrl"];
	        this.error = this.convertValues(source["error"], MessageError);
	        this.encrypt = this.convertValues(source["encrypt"], Progress);
	        this.send = this.convertValues(source["send"], Progress);
	        this.receive = this.convertValues(source["receive"], Progress);
	        this.decrypt = this.convertValues(source["decrypt"], Progress);
	        this.cancellable = source["cancellable"];
	        this.retryable = source["retryable"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MessagePage {
	    messages: MessageView[];
	    hasMore: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MessagePage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messages = this.convertValues(source["messages"], MessageView);
	        this.hasMore = source["hasMore"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class SessionView {
	    login: string;
	    userId: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.login = source["login"];
	        this.userId = source["userId"];
	    }
	}
	export class Settings {
	    serverAddress: string;
	    certificatePath: string;
	    recentLogins: string[];
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.serverAddress = source["serverAddress"];
	        this.certificatePath = source["certificatePath"];
	        this.recentLogins = source["recentLogins"];
	    }
	}
	export class Snapshot {
	    version: number;
	    connection: string;
	    session?: SessionView;
	    contacts: string[];
	    incoming: string[];
	    outgoing: string[];
	    chats: ChatView[];
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.connection = source["connection"];
	        this.session = this.convertValues(source["session"], SessionView);
	        this.contacts = source["contacts"];
	        this.incoming = source["incoming"];
	        this.outgoing = source["outgoing"];
	        this.chats = this.convertValues(source["chats"], ChatView);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

