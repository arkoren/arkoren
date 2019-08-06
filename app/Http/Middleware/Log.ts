import { Middleware, Request, Response, PossibleResponse } from '../../../../framework/http.ts'

/**
 * Implements the Log middleware.
 *
 * @export
 * @class Log
 * @implements {Middleware}
 */
export class Log extends Middleware {

    /**
     * Runs before the route handler.
     *
     * @param {Request} request
     * @returns {PossibleResponse}
     * @memberof Log
     */
    before(request: Request): PossibleResponse {
        console.log('Log before...')
    }

    /**
     * Runs after the route handler.
     *
     * @param {Request} request
     * @param {Response} response
     * @returns {PossibleResponse}
     * @memberof Log
     */
    after(request: Request, response: Response): PossibleResponse {
        console.log('Log after...')
    }

}
