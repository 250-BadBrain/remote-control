export namespace main {
	
	export class frontendICEServer {
	    urls: string[];
	    username?: string;
	    credential?: string;
	
	    static createFrom(source: any = {}) {
	        return new frontendICEServer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.urls = source["urls"];
	        this.username = source["username"];
	        this.credential = source["credential"];
	    }
	}

}

