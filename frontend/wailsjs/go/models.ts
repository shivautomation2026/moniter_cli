export namespace main {
	
	export class Config {
	    pdf_folder: string;
	    base_output_folder: string;
	    source_token: string;
	    ingest_url: string;
	    s3_bucket_name: string;
	    s3_endpoint_url: string;
	    s3_region: string;
	    s3_access_key: string;
	    s3_secret_key: string;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pdf_folder = source["pdf_folder"];
	        this.base_output_folder = source["base_output_folder"];
	        this.source_token = source["source_token"];
	        this.ingest_url = source["ingest_url"];
	        this.s3_bucket_name = source["s3_bucket_name"];
	        this.s3_endpoint_url = source["s3_endpoint_url"];
	        this.s3_region = source["s3_region"];
	        this.s3_access_key = source["s3_access_key"];
	        this.s3_secret_key = source["s3_secret_key"];
	    }
	}

}

